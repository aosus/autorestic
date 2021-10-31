package internal

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cupcakearmy/autorestic/internal/colors"
	"github.com/cupcakearmy/autorestic/internal/lock"
	"github.com/joho/godotenv"
	"github.com/mitchellh/go-homedir"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const VERSION = "1.4.1"

var CI bool = false
var VERBOSE bool = false
var CRON_LEAN bool = false

type OptionMap map[string][]interface{}
type Options map[string]OptionMap

type Config struct {
	Extras    interface{}         `yaml:"extras"`
	Locations map[string]Location `yaml:"locations"`
	Backends  map[string]Backend  `yaml:"backends"`
	Global    Options             `yaml:"global"`
}

var once sync.Once
var config *Config

func GetConfig() *Config {
	if config == nil {
		once.Do(func() {
			if err := viper.ReadInConfig(); err == nil {
				if !CRON_LEAN {
					absConfig, _ := filepath.Abs(viper.ConfigFileUsed())
					colors.Faint.Println("Using config: \t", absConfig)
					// Load env file
					envFile := filepath.Join(filepath.Dir(absConfig), ".autorestic.env")
					err = godotenv.Load(envFile)
					if err == nil {
						colors.Faint.Println("Using env:\t", envFile)
					}
				}
			} else {
				return
			}

			config = &Config{}
			if err := viper.UnmarshalExact(config); err != nil {
				colors.Error.Println(err)
				colors.Error.Println("Could not parse config file!")
				lock.Unlock()
				os.Exit(1)
			}
		})
	}
	return config
}

func GetPathRelativeToConfig(p string) (string, error) {
	if path.IsAbs(p) {
		return p, nil
	} else if strings.HasPrefix(p, "~") {
		home, err := homedir.Dir()
		return path.Join(home, strings.TrimPrefix(p, "~")), err
	} else {
		return path.Join(path.Dir(viper.ConfigFileUsed()), p), nil
	}
}

func (c *Config) Describe() {
	// Locations
	for name, l := range c.Locations {
		var tmp string
		colors.PrimaryPrint(`Location: "%s"`, name)

		tmp = ""
		for _, path := range l.From {
			tmp += fmt.Sprintf("\t%s %s\n", colors.Success.Sprint("←"), path)
		}
		colors.PrintDescription("From", tmp)

		tmp = ""
		for _, to := range l.To {
			tmp += fmt.Sprintf("\t%s %s\n", colors.Success.Sprint("→"), to)
		}
		colors.PrintDescription("To", tmp)

		if l.Cron != "" {
			colors.PrintDescription("Cron", l.Cron)
		}

		tmp = ""
		hooks := map[string][]string{
			"Before":  l.Hooks.Before,
			"After":   l.Hooks.After,
			"Failure": l.Hooks.Failure,
			"Success": l.Hooks.Success,
		}
		for hook, commands := range hooks {
			if len(commands) > 0 {
				tmp += "\n\t" + hook
				for _, cmd := range commands {
					tmp += colors.Faint.Sprintf("\n\t  ▶ %s", cmd)
				}
			}
		}
		if tmp != "" {
			colors.PrintDescription("Hooks", tmp)
		}

		if len(l.Options) > 0 {
			tmp = ""
			for t, options := range l.Options {
				tmp += "\n\t" + t
				for option, values := range options {
					for _, value := range values {
						tmp += colors.Faint.Sprintf("\n\t  ✧ --%s=%s", option, value)
					}
				}
			}
			colors.PrintDescription("Options", tmp)
		}
	}

	// Backends
	for name, b := range c.Backends {
		colors.PrimaryPrint("Backend: \"%s\"", name)
		colors.PrintDescription("Type", b.Type)
		colors.PrintDescription("Path", b.Path)

		if len(b.Env) > 0 {
			tmp := ""
			for option, value := range b.Env {
				tmp += fmt.Sprintf("\n\t%s %s %s", colors.Success.Sprint("✧"), strings.ToUpper(option), colors.Faint.Sprint(value))
			}
			colors.PrintDescription("Env", tmp)
		}
	}
}

func CheckConfig() error {
	c := GetConfig()
	if c == nil {
		return fmt.Errorf("config could not be loaded/found")
	}
	if !CheckIfResticIsCallable() {
		return fmt.Errorf(`%s was not found. Install either with "autorestic install" or manually`, RESTIC_BIN)
	}
	for name, backend := range c.Backends {
		backend.name = name
		if err := backend.validate(); err != nil {
			return err
		}
	}
	for name, location := range c.Locations {
		location.name = name
		if err := location.validate(); err != nil {
			return err
		}
	}
	return nil
}

func GetAllOrSelected(cmd *cobra.Command, backends bool) ([]string, error) {
	var list []string
	if backends {
		for name := range config.Backends {
			list = append(list, name)
		}
	} else {
		for name := range config.Locations {
			list = append(list, name)
		}
	}

	all, _ := cmd.Flags().GetBool("all")
	if all {
		return list, nil
	}

	var selected []string
	if backends {
		selected, _ = cmd.Flags().GetStringSlice("backend")
	} else {
		selected, _ = cmd.Flags().GetStringSlice("location")
	}
	for _, s := range selected {
		var splitted = strings.Split(s, "@")
		for _, l := range list {
			if l == splitted[0] {
				goto found
			}
		}
		if backends {
			return nil, fmt.Errorf("invalid backend \"%s\"", s)
		} else {
			return nil, fmt.Errorf("invalid location \"%s\"", s)
		}
	found:
	}

	if len(selected) == 0 {
		return selected, fmt.Errorf("nothing selected, aborting")
	}
	return selected, nil
}

func AddFlagsToCommand(cmd *cobra.Command, backend bool) {
	var usage string
	if backend {
		usage = "all backends"
	} else {
		usage = "all locations"
	}
	cmd.PersistentFlags().BoolP("all", "a", false, usage)
	if backend {
		cmd.PersistentFlags().StringSliceP("backend", "b", []string{}, "select backends")
	} else {
		cmd.PersistentFlags().StringSliceP("location", "l", []string{}, "select locations")
	}
}

func (c *Config) SaveConfig() error {
	file := viper.ConfigFileUsed()
	if err := CopyFile(file, file+".old"); err != nil {
		return err
	}
	colors.Secondary.Println("Saved a backup copy of your file next the the original.")

	viper.Set("backends", c.Backends)
	viper.Set("locations", c.Locations)

	return viper.WriteConfig()
}

func optionToString(option string) string {
	if !strings.HasPrefix(option, "-") {
		return "--" + option
	}
	return option
}

func appendOptionsToSlice(str *[]string, options OptionMap) {
	for key, values := range options {
		for _, value := range values {
			// Bool
			asBool, ok := value.(bool)
			if ok && asBool {
				*str = append(*str, optionToString(key))
				continue
			}
			*str = append(*str, optionToString(key), fmt.Sprint(value))
		}
	}
}

func getOptions(options Options, key string) []string {
	var selected []string
	var keys = []string{"all"}
	if key != "" {
		keys = append(keys, key)
	}
	for _, key := range keys {
		appendOptionsToSlice(&selected, options[key])
	}
	return selected
}

func combineOptions(key string, l Location, b Backend) []string {
	// Priority: location > backend > global
	var options []string
	gFlags := getOptions(GetConfig().Global, key)
	bFlags := getOptions(b.Options, key)
	lFlags := getOptions(l.Options, key)
	options = append(options, gFlags...)
	options = append(options, bFlags...)
	options = append(options, lFlags...)
	return options
}
