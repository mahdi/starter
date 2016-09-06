package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/bugsnag/bugsnag-go"
	"github.com/cloud66/starter/common"
	"github.com/mitchellh/go-homedir"
)

type downloadFile struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

type analysisResult struct {
	Warnings         []string
	OK               bool
	Language         string
	Framework        string
	FrameworkVersion string
}

type templateDefinition struct {
	Version           string         `json:"version"`
	Dockerfiles       []downloadFile `json:"dockerfiles"`
	ServiceYmls       []downloadFile `json:"service-ymls"`
	DockerComposeYmls []downloadFile `json:"docker-compose-ymls"`
}

var (
	flagPath        string
	flagNoPrompt    bool
	flagEnvironment string
	flagTemplates   string
	flagBranch      string
	flagVersion     string
	flagGenerator   string
	flagOverwrite   bool
	flagConfig      string
	flagDaemon      bool

	config = &Config{}

	// VERSION holds the starter version
	VERSION = "dev"
	// BUILDDATE holds the date starter was built
	BUILDDATE string

	serviceYAMLTemplateDir       string
	dockerfileTemplateDir        string
	dockerComposeYAMLTemplateDir string
)

const (
	templatePath = "https://raw.githubusercontent.com/cloud66/starter/{{.branch}}/templates/templates.json"
)

func init() {
	bugsnag.Configure(bugsnag.Configuration{
		APIKey:     "916591d12b54e689edde67e641c5843d",
		AppVersion: VERSION,
	})

	flag.StringVar(&flagPath, "p", "", "project path")
	flag.StringVar(&flagConfig, "c", "", "configuration path for the daemon mode")
	flag.BoolVar(&flagNoPrompt, "y", false, "do not prompt user")
	flag.BoolVar(&flagOverwrite, "overwrite", false, "overwrite existing files")
	flag.StringVar(&flagEnvironment, "e", "production", "set project environment")
	flag.StringVar(&flagTemplates, "templates", "", "location of the templates directory")
	flag.StringVar(&flagBranch, "branch", "master", "template branch in github")
	flag.BoolVar(&flagDaemon, "daemon", false, "runs Starter in daemon mode")
	flag.StringVar(&flagVersion, "v", "", "version of starter")
	flag.StringVar(&flagGenerator, "g", "dockerfile", `what kind of files need to be generated by starter:
	-g dockerfile: only the Dockerfile
	-g docker-compose: only the docker-compose.yml + Dockerfile
	-g service: only the service.yml + Dockerfile (cloud 66 specific)
	-g dockerfile,service,docker-compose (all files)`)
}

// downloading templates from github and putting them into homedir
func getTempaltes(tempDir string) error {
	common.PrintlnL0("Checking templates in %s", tempDir)

	var tv templateDefinition
	err := fetchJSON(strings.Replace(templatePath, "{{.branch}}", flagBranch, -1), nil, &tv)
	if err != nil {
		return err
	}

	// is there a local copy?
	if _, err := os.Stat(filepath.Join(tempDir, "templates.json")); os.IsNotExist(err) {
		// no file. downloading
		common.PrintlnL1("No local templates found. Downloading now.")
		err := os.MkdirAll(tempDir, 0777)
		if err != nil {
			return err
		}

		err = downloadTemplates(tempDir, tv)
		if err != nil {
			return err
		}
	}

	// load the local json
	templatesLocal, err := ioutil.ReadFile(filepath.Join(tempDir, "templates.json"))
	if err != nil {
		return err
	}
	var localTv templateDefinition
	err = json.Unmarshal(templatesLocal, &localTv)
	if err != nil {
		return err
	}

	// compare
	if localTv.Version != tv.Version {
		common.PrintlnL2("Newer templates found. Downloading them now")
		// they are different, we need to download the new ones
		err = downloadTemplates(tempDir, tv)
		if err != nil {
			return err
		}
	} else {
		common.PrintlnL1("Local templates are up to date")
	}

	return nil
}

func downloadTemplates(tempDir string, td templateDefinition) error {
	err := downloadSingleFile(tempDir, downloadFile{URL: strings.Replace(templatePath, "{{.branch}}", flagBranch, -1), Name: "templates.json"})
	if err != nil {
		return err
	}

	for _, temp := range td.Dockerfiles {
		err := downloadSingleFile(tempDir, temp)
		if err != nil {
			return err
		}
	}

	for _, temp := range td.ServiceYmls {
		err := downloadSingleFile(tempDir, temp)
		if err != nil {
			return err
		}
	}

	for _, temp := range td.DockerComposeYmls {
		err := downloadSingleFile(tempDir, temp)
		if err != nil {
			return err
		}
	}

	return nil
}

func downloadSingleFile(tempDir string, temp downloadFile) error {
	r, err := fetch(strings.Replace(temp.URL, "{{.branch}}", flagBranch, -1), nil)
	if err != nil {
		return err
	}
	defer r.Close()

	output, err := os.Create(filepath.Join(tempDir, temp.Name))
	if err != nil {
		return err
	}
	defer output.Close()

	_, err = io.Copy(output, r)
	if err != nil {
		return err
	}

	return nil
}

func main() {
	args := os.Args[1:]

	if len(args) > 0 && (args[0] == "help" || args[0] == "-h") {
		fmt.Printf("Starter (%s) Help\n", VERSION)
		flag.PrintDefaults()
		return
	}

	if len(args) > 0 && (args[0] == "version" || args[0] == "-v") {
		fmt.Printf("Starter version: %s (%s)\n", VERSION, BUILDDATE)
		return
	}

	flag.Parse()

	if flagDaemon && flagConfig != "" {
		if _, err := os.Stat(flagConfig); os.IsNotExist(err) {
			common.PrintError("Configuration directory not found: %s", flagConfig)
			os.Exit(1)
		}

		common.PrintL0("Using %s for configuration", flagConfig)
		conf, err := ReadFromFile(flagConfig)
		if err != nil {
			common.PrintError("Failed to load configuration file due to %s", err.Error())
			os.Exit(1)
		}
		*config = *conf
	} else {
		config.SetDefaults()
	}

	common.PrintlnTitle("Starter (c) 2016 Cloud66 Inc.")

	// Run in daemon mode
	if flagDaemon {
		signalChan := make(chan os.Signal, 1)
		cleanupDone := make(chan bool)
		signal.Notify(signalChan, os.Interrupt)
		config.template_path = flagTemplates

		api := NewAPI(config)
		err := api.StartAPI()
		if err != nil {
			common.PrintError("Unable to start the API due to %s", err.Error())
			os.Exit(1)
		}

		go func() {
			for _ = range signalChan {
				common.PrintL0("Received an interrupt, stopping services\n")
				cleanupDone <- true
			}
		}()

		<-cleanupDone
		os.Exit(0)
	}

	result, err := analyze(
		true,
		flagPath,
		flagTemplates,
		flagEnvironment,
		flagNoPrompt,
		flagOverwrite,
		flagGenerator)

	if err != nil {
		common.PrintError(err.Error())
		os.Exit(1)
	}
	if len(result.Warnings) > 0 {
		common.PrintlnWarning("Warnings:")
		for _, warning := range result.Warnings {
			common.PrintlnWarning(" * " + warning)
		}
	}

	common.PrintlnL0("Now you can add the newly created Dockerfile to your git")
	common.PrintlnL0("To do that you will need to run the following commands:\n\n")
	fmt.Printf("cd %s\n", flagPath)
	fmt.Println("git add Dockerfile")
	fmt.Println("git commit -m 'Adding Dockerfile'")
	if strings.Contains(flagGenerator, "service") {
		common.PrintlnL0("To create a new Docker Stack with Cloud 66 use the following command:\n\n")
		fmt.Printf("cx stacks create --name='CHANGEME' --environment='%s' --service_yaml=service.yml\n\n", flagEnvironment)
	}

	common.PrintlnTitle("Done")
}

func analyze(
	updateTemplates bool,
	path string,
	templates string,
	environment string,
	noPrompt bool,
	overwrite bool,
	generator string) (*analysisResult, error) {

	if path == "" {
		pwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("Unable to detect current directory path due to %s", err.Error())
		}
		path = pwd
	}

	result := &analysisResult{OK: false}

	// if templateFolder is specified we're going to use that otherwise download
	if templates == "" {
		homeDir, _ := homedir.Dir()

		templates = filepath.Join(homeDir, ".starter")
		if updateTemplates {
			err := getTempaltes(templates)
			if err != nil {
				return nil, fmt.Errorf("Failed to download latest templates due to %s", err.Error())
			}
		}

		dockerfileTemplateDir = templates
		serviceYAMLTemplateDir = templates
		dockerComposeYAMLTemplateDir = templates

	} else {
		common.PrintlnTitle("Using local templates at %s", templates)
		templates, err := filepath.Abs(templates)
		if err != nil {
			return nil, fmt.Errorf("Failed to use %s for templates due to %s", templates, err.Error())
		}
		dockerfileTemplateDir = templates
		serviceYAMLTemplateDir = templates
		dockerComposeYAMLTemplateDir = templates
	}

	common.PrintlnTitle("Detecting framework for the project at %s", path)

	pack, err := Detect(path)
	if err != nil {
		return nil, fmt.Errorf("Failed to detect framework due to: %s", err.Error())
	}

	// check for Dockerfile (before analysis to avoid wasting time)
	dockerfilePath := filepath.Join(path, "Dockerfile")
	if _, err := os.Stat(dockerfilePath); err == nil {
		// file exists. should we overwrite?
		if !overwrite {
			return nil, errors.New("Dockerfile already exists. Use overwrite flag to overwrite it")
		}
	}

	serviceYAMLPath := filepath.Join(path, "service.yml")
	if _, err := os.Stat(serviceYAMLPath); err == nil {
		// file exists. should we overwrite?
		if !overwrite {
			return nil, errors.New("service.yml already exists. Use overwrite flag to overwrite it")
		}
	}

	err = pack.Analyze(path, environment, !noPrompt)
	if err != nil {
		return nil, fmt.Errorf("Failed to analyze the project due to: %s", err.Error())
	}

	err = pack.WriteDockerfile(dockerfileTemplateDir, path, !noPrompt)
	if err != nil {
		return nil, fmt.Errorf("Failed to write Dockerfile due to: %s", err.Error())
	}

	if strings.Contains(generator, "service") {
		err = pack.WriteServiceYAML(serviceYAMLTemplateDir, path, !noPrompt)
		if err != nil {
			return nil, fmt.Errorf("Failed to write service.yml due to: %s", err.Error())
		}
	}

	if strings.Contains(generator, "docker-compose") {
		err = pack.WriteDockerComposeYAML(dockerComposeYAMLTemplateDir, path, !noPrompt)
		if err != nil {
			return nil, fmt.Errorf("Failed to write docker-compose.yml due to: %s", err.Error())
		}
	}

	if len(pack.GetMessages()) > 0 {
		for _, warning := range pack.GetMessages() {
			result.Warnings = append(result.Warnings, warning)
		}
	}

	result.OK = true
	result.Language = pack.Name()
	result.Framework = pack.Framework()
	result.FrameworkVersion = pack.FrameworkVersion()

	return result, nil
}
