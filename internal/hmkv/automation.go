package hmkv

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

type AutomationParamSource string

const (
	ParamSourcePrompt     AutomationParamSource = "prompt"
	ParamSourceStatic     AutomationParamSource = "static"
	ParamSourceHMKVOutput AutomationParamSource = "hmkv_output"
)

var validParamSources = []string{
	string(ParamSourcePrompt),
	string(ParamSourceStatic),
	string(ParamSourceHMKVOutput),
}

var validHMKVKeys = []string{
	"hb_output_dir",
	"mkv_output_dir",
	"run_duration",
	"raw_files_deleted",
	"title_count",
	"total_raw_size",
	"total_encoded_size",
}

type AutomationParam struct {
	Name         string                `json:"name"`
	Description  string                `json:"description"`
	Source       AutomationParamSource `json:"source"`
	DefaultValue string                `json:"default_value,omitempty"`
	HMKVKey      string                `json:"hmkv_key,omitempty"`
}

type Automation struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Params  []AutomationParam `json:"params,omitempty"`
}

// validateAutomationName rejects names that could escape the automations directory.
func validateAutomationName(name string) error {
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return fmt.Errorf("automation name '%s' contains invalid characters (no path separators or '..' allowed)", name)
	}
	return nil
}

// getAutomationsDir returns the OS-appropriate automations directory path.
func getAutomationsDir() (string, error) {
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", fmt.Errorf("APPDATA environment variable is not set")
		}
		return filepath.Join(appData, "handymkv", "automations"), nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not get home directory: %w", err)
	}
	return filepath.Join(homeDir, ".config", "handymkv", "automations"), nil
}

// ListAutomations reads all automation JSON files from the automations directory.
func ListAutomations() ([]Automation, error) {
	dir, err := getAutomationsDir()
	if err != nil {
		return nil, err
	}

	pattern := filepath.Join(dir, "*.json")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("error scanning automations directory: %w", err)
	}

	var automations []Automation
	for _, f := range files {
		a, err := readAutomationFile(f)
		if err != nil {
			fmt.Printf("Warning: could not load automation file '%s': %v\n", filepath.Base(f), err)
			continue
		}
		automations = append(automations, *a)
	}

	return automations, nil
}

// LoadAutomation reads a single automation by name.
func LoadAutomation(name string) (*Automation, error) {
	if err := validateAutomationName(name); err != nil {
		return nil, err
	}

	dir, err := getAutomationsDir()
	if err != nil {
		return nil, err
	}

	filePath := filepath.Join(dir, name+".json")
	return readAutomationFile(filePath)
}

func readAutomationFile(path string) (*Automation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var a Automation
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	if a.Command == "" {
		return nil, fmt.Errorf("automation '%s' has an empty command field", a.Name)
	}
	return &a, nil
}

// SaveAutomation writes an automation JSON file to the automations directory.
func SaveAutomation(a *Automation) error {
	dir, err := getAutomationsDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0740); err != nil {
		return fmt.Errorf("could not create automations directory: %w", err)
	}

	filePath := filepath.Join(dir, a.Name+".json")

	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("could not marshal automation: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0640); err != nil {
		return fmt.Errorf("could not write automation file: %w", err)
	}

	return nil
}

// DeleteAutomation deletes an automation file after confirmation.
func DeleteAutomation(name string) error {
	if err := validateAutomationName(name); err != nil {
		return err
	}

	dir, err := getAutomationsDir()
	if err != nil {
		return err
	}

	filePath := filepath.Join(dir, name+".json")

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		fmt.Printf("Automation '%s' not found.\n\n", name)
		return nil
	}

	fmt.Printf("Delete automation '%s'? [y/N]: ", name)

	if strings.ToLower(readLine()) != "y" {
		fmt.Println("Aborted.")
		return nil
	}

	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("could not delete automation: %w", err)
	}

	fmt.Printf("Automation '%s' deleted.\n\n", name)

	if configPath, err := GetConfigFilePath(); err == nil {
		if changed, err := setAlwaysRunAutomation(configPath, name, false); err != nil {
			fmt.Printf("Warning: could not remove '%s' from the automations in %s: %v\n\n", name, configPath, err)
		} else if changed {
			fmt.Printf("Removed '%s' from the automations that always run (%s).\n\n", name, configPath)
		}
	}

	return nil
}

// PrintAutomations lists all saved automations.
func PrintAutomations() error {
	automations, err := ListAutomations()
	if err != nil {
		return err
	}

	if len(automations) == 0 {
		fmt.Println("No automations found.")
		fmt.Println("Run 'handymkv automations create' to create one.")
		fmt.Println()
		return nil
	}

	fmt.Printf("Saved Automations:\n\n")

	for i, a := range automations {
		paramCount := len(a.Params)
		paramLabel := "params"
		if paramCount == 1 {
			paramLabel = "param"
		}
		fmt.Printf("  %d. %s - %s (%d %s)\n", i+1, a.Name, a.Command, paramCount, paramLabel)
	}

	fmt.Printf("\nRun 'handymkv automations show <name>' to view details.\n\n")
	return nil
}

// PrintAutomation shows the details of a single automation.
func PrintAutomation(name string) error {
	a, err := LoadAutomation(name)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("Automation '%s' not found.\n\n", name)
			return nil
		}
		return err
	}

	fmt.Printf("Automation: %s\n", a.Name)
	fmt.Printf("Command:    %s\n", a.Command)

	if len(a.Params) == 0 {
		fmt.Printf("Parameters: none\n")
	} else {
		fmt.Printf("Parameters:\n\n")
		for _, p := range a.Params {
			envVarName := "HMKV_PARAM_" + strings.ToUpper(p.Name)
			fmt.Printf("  %s -> %s\n", p.Name, envVarName)
			fmt.Printf("    Description: %s\n", p.Description)
			fmt.Printf("    Source:      %s\n", p.Source)

			switch p.Source {
			case ParamSourcePrompt:
				if p.DefaultValue != "" {
					fmt.Printf("    Default:     %s\n", p.DefaultValue)
				}
			case ParamSourceStatic:
				fmt.Printf("    Value:       %s\n", p.DefaultValue)
			case ParamSourceHMKVOutput:
				fmt.Printf("    HMKV Key:    %s\n", p.HMKVKey)
			}

			fmt.Println()
		}
	}

	fmt.Println()
	return nil
}

// CreateAutomation runs the interactive wizard to create a new automation.
func CreateAutomation() error {
	fmt.Println("Create a new automation")
	fmt.Println()
	fmt.Println("Note: Automation scripts receive parameters as environment variables.")
	fmt.Println("Each parameter is available as HMKV_PARAM_<UPPERCASED_NAME>.")
	fmt.Println()

	name := promptForString(
		"Automation name:",
		"A short identifier for this automation.",
		"",
		nil,
	)

	if name == "" {
		fmt.Println("Name is required.")
		return nil
	}

	// Sanitize name for use as filename
	name = strings.ReplaceAll(strings.TrimSpace(name), " ", "-")

	if err := validateAutomationName(name); err != nil {
		fmt.Printf("Invalid name: %v\n", err)
		return nil
	}

	command := promptForString(
		"Command to execute:",
		"The script or command that will be run after encoding completes.",
		"",
		nil,
	)

	if command == "" {
		fmt.Println("Command is required.")
		return nil
	}

	var params []AutomationParam

	for {
		addParam := promptForBool("Add a parameter?", "", false)

		if !addParam {
			break
		}

		clear()

		param := AutomationParam{}

		param.Name = promptForString("Parameter name:", "A short identifier (becomes HMKV_PARAM_<UPPERCASED_NAME>).", "", nil)
		if param.Name == "" {
			fmt.Println("Parameter name is required. Skipping.")
			continue
		}

		param.Description = promptForString("Description:", "Explain what this parameter is for.", "", nil)

		source := promptForSelection("Parameter source:", validParamSources)
		param.Source = AutomationParamSource(source)

		switch param.Source {
		case ParamSourcePrompt:
			param.DefaultValue = promptForString("Default value (optional):", "Shown to user when prompted. Leave blank for no default.", "", nil)
		case ParamSourceStatic:
			param.DefaultValue = promptForString("Static value:", "The hardcoded value for this parameter.", "", nil)
			if param.DefaultValue == "" {
				fmt.Println("Default value is required. Skipping parameter.")
				continue
			}
		case ParamSourceHMKVOutput:
			param.HMKVKey = promptForSelection("HandyMKV output key:", validHMKVKeys)
		}

		params = append(params, param)
		clear()
	}

	a := &Automation{
		Name:    name,
		Command: command,
		Params:  params,
	}

	alwaysRun := promptForBool(
		"Run this automation after every run?",
		"If enabled, the automation is added to \"automations\" in config.json and runs after every run without -a.",
		false,
	)

	if err := SaveAutomation(a); err != nil {
		return fmt.Errorf("could not save automation: %w", err)
	}

	dir, _ := getAutomationsDir()
	fmt.Printf("Automation '%s' saved to %s\n\n", a.Name, filepath.Join(dir, a.Name+".json"))

	if alwaysRun {
		configPath, err := GetConfigFilePath()
		if err != nil {
			fmt.Printf("Warning: no config file found, so '%s' could not be set to always run. Run 'handymkv config setup' first and add it to \"automations\" in config.json.\n\n", a.Name)
			return nil
		}

		if _, err := setAlwaysRunAutomation(configPath, a.Name, true); err != nil {
			fmt.Printf("Warning: could not add '%s' to the automations in %s: %v\n\n", a.Name, configPath, err)
			return nil
		}

		fmt.Printf("Automation '%s' runs after every run (%s).\n\n", a.Name, configPath)
	}

	return nil
}

// setAlwaysRunAutomation adds the automation to (enabled) or removes it from the "automations"
// list of the config file. All other settings are kept as they are. It reports whether the file
// was changed.
func setAlwaysRunAutomation(configPath, name string, enabled bool) (bool, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, fmt.Errorf("could not parse the config file: %w", err)
	}
	if raw == nil {
		raw = make(map[string]json.RawMessage)
	}

	var names []string
	if existing, ok := raw["automations"]; ok {
		if err := json.Unmarshal(existing, &names); err != nil {
			return false, fmt.Errorf("\"automations\" is not a list of names: %w", err)
		}
	}

	contains := slices.Contains(names, name)

	switch {
	case enabled && !contains:
		names = append(names, name)
	case !enabled && contains:
		names = slices.DeleteFunc(names, func(n string) bool { return n == name })
	default:
		return false, nil
	}

	if len(names) == 0 {
		delete(raw, "automations")
	} else {
		encoded, err := json.Marshal(names)
		if err != nil {
			return false, err
		}
		raw["automations"] = encoded
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return false, err
	}

	if err := os.WriteFile(configPath, out, 0640); err != nil {
		return false, err
	}

	return true, nil
}

// resolvePreRunParams collects prompt/env/default param values before the run starts.
// Deduplicates by param name across automations.
func resolvePreRunParams(automations []Automation) (map[string]string, error) {
	resolved := make(map[string]string)

	for _, a := range automations {
		for _, p := range a.Params {
			if _, exists := resolved[p.Name]; exists {
				continue
			}

			switch p.Source {
			case ParamSourcePrompt:
				val := promptForString(
					fmt.Sprintf("[%s] %s:", a.Name, p.Description),
					"",
					p.DefaultValue,
					nil,
				)
				resolved[p.Name] = val

			case ParamSourceStatic:
				resolved[p.Name] = p.DefaultValue

			case ParamSourceHMKVOutput:
				// Resolved after the run completes; skip here
				continue
			}
		}
	}

	return resolved, nil
}

// buildRunOutputData maps hmkv_key values to run output data.
func buildRunOutputData(
	hbDir string,
	mkvDir string,
	duration time.Duration,
	titleCount int,
	rawDeleted bool,
	totalRaw int64,
	totalEncoded int64,
) map[string]string {
	rawDeletedStr := "false"
	if rawDeleted {
		rawDeletedStr = "true"
	}

	return map[string]string{
		"hb_output_dir":      hbDir,
		"mkv_output_dir":     mkvDir,
		"run_duration":       formatTimeElapsedString(duration),
		"raw_files_deleted":  rawDeletedStr,
		"title_count":        fmt.Sprintf("%d", titleCount),
		"total_raw_size":     fmt.Sprintf("%d", totalRaw),
		"total_encoded_size": fmt.Sprintf("%d", totalEncoded),
	}
}

// RunAutomations executes selected automations with resolved parameters.
// Returns manifest entries recording each automation's name, command, and resolved param values.
func RunAutomations(automations []Automation, preRunParams map[string]string, outputData map[string]string) []manifestAutomation {
	if len(automations) == 0 {
		return nil
	}

	fmt.Println("\nRunning automations...")

	var entries []manifestAutomation

	for _, a := range automations {
		// Build environment and collect resolved params for manifest
		env := os.Environ()
		var manifestParams []manifestAutomationParam

		for _, p := range a.Params {
			envKey := "HMKV_PARAM_" + strings.ToUpper(p.Name)
			var val string

			if p.Source == ParamSourceHMKVOutput {
				val = outputData[p.HMKVKey]
			} else {
				val = preRunParams[p.Name]
			}

			env = append(env, fmt.Sprintf("%s=%s", envKey, val))
			manifestParams = append(manifestParams, manifestAutomationParam{
				Name:  p.Name,
				Value: val,
			})
		}

		// Execute via shell
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("cmd", "/c", a.Command)
		} else {
			cmd = exec.Command("sh", "-c", a.Command)
		}

		cmd.Env = env

		exitCode := 0
		err := cmd.Run()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
				fmt.Printf("  %s: FAILED (exit code %d)\n", a.Name, exitCode)
			} else {
				exitCode = -1
				fmt.Printf("  %s: FAILED (%v)\n", a.Name, err)
			}
		} else {
			fmt.Printf("  %s: OK\n", a.Name)
		}

		entries = append(entries, manifestAutomation{
			Name:     a.Name,
			Command:  a.Command,
			Params:   manifestParams,
			ExitCode: exitCode,
		})
	}

	fmt.Println()
	return entries
}

// mergeAutomationNames returns the automations configured to always run followed by the ones
// given for this run, trimmed and without empty names or duplicates.
func mergeAutomationNames(configured, requested []string) []string {
	seen := make(map[string]struct{})
	var names []string

	for _, list := range [][]string{configured, requested} {
		for _, name := range list {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}

	return names
}
