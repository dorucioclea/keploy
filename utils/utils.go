package utils

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.keploy.io/server/v2/pkg/platform/yaml/configdb"

	"github.com/TheZeroSlave/zapsentry"
	"github.com/getsentry/sentry-go"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// var ErrFileNotFound = errors.New("file Not found")
var WarningSign = "\U000026A0"

func BindFlagsToViper(logger *zap.Logger, cmd *cobra.Command, viperKeyPrefix string) {
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		// Construct the Viper key and the env variable name
		if viperKeyPrefix == "" {
			viperKeyPrefix = cmd.Name()
		}
		viper.SetEnvPrefix("KEPLOY")
		viperKey := viperKeyPrefix + "." + flag.Name
		envVarName := strings.ToUpper(viperKeyPrefix + "_" + flag.Name)
		envVarName = strings.ReplaceAll(envVarName, ".", "_") // Why do we need this?

		// Bind the flag to Viper with the constructed key
		err := viper.BindPFlag(viperKey, flag)
		if err != nil {
			logger.Error("Failed to bind flag to config", zap.Error(err))
		}

		// Tell Viper to also read this flag's value from the corresponding env variable
		err = viper.BindEnv(viperKey, envVarName)
		if err != nil {
			logger.Error("Failed to bind environment variables to config", zap.Error(err))

		}
	})
}

func ModifyToSentryLogger(ctx context.Context, logger *zap.Logger, client *sentry.Client, configDb *configdb.ConfigDb) *zap.Logger {
	cfg := zapsentry.Configuration{
		Level:             zapcore.ErrorLevel, //when to send message to sentry
		EnableBreadcrumbs: true,               // enable sending breadcrumbs to Sentry
		BreadcrumbLevel:   zapcore.InfoLevel,  // at what level should we sent breadcrumbs to sentry
		Tags: map[string]string{
			"component": "system",
		},
	}

	core, err := zapsentry.NewCore(cfg, zapsentry.NewSentryClientFromClient(client))
	//in case of err it will return noop core. So we don't need to attach it to log.
	if err != nil {
		logger.Debug("failed to init zap", zap.Error(err))
		return logger
	}

	logger = zapsentry.AttachCoreToLogger(core, logger)
	kernelVersion := ""
	if runtime.GOOS == "linux" {
		cmd := exec.Command("uname", "-r")
		kernelBytes, err := cmd.Output()
		if err != nil {
			logger.Debug("failed to get kernel version", zap.Error(err))
		} else {
			kernelVersion = string(kernelBytes)
		}
	}

	arch := runtime.GOARCH
	installationID, err := configDb.GetInstallationId(ctx)
	if err != nil {
		logger.Debug("failed to get installationID", zap.Error(err))
	}
	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetTag("Keploy Version", Version)
		scope.SetTag("Linux Kernel Version", kernelVersion)
		scope.SetTag("Architecture", arch)
		scope.SetTag("Installation ID", installationID)
	})
	return logger
}

type GitHubRelease struct {
	TagName string `json:"tag_name"`
	Body    string `json:"body"`
}

var ErrGitHubAPIUnresponsive = errors.New("GitHub API is unresponsive")

var Emoji = "\U0001F430" + " Keploy:"
var ConfigGuide = `
# Example on using tests
#tests:
#  filters:
#   - path: "/user/app"
#     urlMethods: ["GET"]
#     headers: {
#       "^asdf*": "^test"
#     }
#     host: "dc.services.visualstudio.com"
#Example on using stubs
#stubs:
#  filters:
#   - path: "/user/app"
#     port: 8080
#   - port: 8081
#   - host: "dc.services.visualstudio.com"
#   - port: 8081
#     host: "dc.services.visualstudio.com"
#     path: "/user/app"
	#
#Example on using globalNoise
#globalNoise:
#   global:
#     body: {
#        # to ignore some values for a field,
#        # pass regex patterns to the corresponding array value
#        "url": ["https?://\S+", "http://\S+"],
#     }
#     header: {
#        # to ignore the entire field, pass an empty array
#        "Date": [],
#      }
#    # to ignore fields or the corresponding values for a specific test-set,
#    # pass the test-set-name as a key to the "test-sets" object and
#    # populate the corresponding "body" and "header" objects
#    test-sets:
#      test-set-1:
#        body: {
#          # ignore all the values for the "url" field
#          "url": []
#        }
#        header: {
#          # we can also pass the exact value to ignore for a field
#          "User-Agent": ["PostmanRuntime/7.34.0"]
#        }
`

// AskForConfirmation asks the user for confirmation. A user must type in "yes" or "no" and
// then press enter. It has fuzzy matching, so "y", "Y", "yes", "YES", and "Yes" all count as
// confirmations. If the input is not recognized, it will ask again. The function does not return
// until it gets a valid response from the user.
func AskForConfirmation(s string) (bool, error) {
	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Printf("%s [y/n]: ", s)

		response, err := reader.ReadString('\n')
		if err != nil {
			return false, err
		}

		response = strings.ToLower(strings.TrimSpace(response))

		if response == "y" || response == "yes" {
			return true, nil
		} else if response == "n" || response == "no" {
			return false, nil
		}
	}
}

func CheckFileExists(path string) bool {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false
	}
	return true
}

var Version string

func attachLogFileToSentry(logFilePath string) error {
	file, err := os.Open(logFilePath)
	if err != nil {
		return errors.New(fmt.Sprintf("Error opening log file: %s", err.Error()))
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return errors.New(fmt.Sprintf("Error reading log file: %s", err.Error()))
	}

	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetExtra("logfile", string(content))
	})
	sentry.Flush(time.Second * 5)
	return nil
}

// Recover recovers from a panic and logs the stack trace to Sentry.
// It also stops the global context.
func Recover(logger *zap.Logger) {
	if logger == nil {
		fmt.Println(Emoji + "Failed to recover from panic. Logger is nil.")
		return
	}
	sentry.Flush(2 * time.Second)
	if r := recover(); r != nil {
		err := attachLogFileToSentry("./keploy-logs.txt")
		if err != nil {
			logger.Error("Failed to attach log file to sentry", zap.Error(err))
		}
		sentry.CaptureException(errors.New(fmt.Sprint(r)))
		// Get the stack trace
		stackTrace := debug.Stack()
		logger.Error("Recovered from:", zap.String("stack trace", string(stackTrace)))
		//stopping the global context
		err = Stop(logger, fmt.Sprintf("Recovered from: %s", r))
		if err != nil {
			logger.Error("Failed to stop the global context", zap.Error(err))
			//return
		}
		sentry.Flush(time.Second * 2)
	}
}

// getLatestGitHubRelease fetches the latest version and release body from GitHub releases with a timeout.
func GetLatestGitHubRelease(ctx context.Context) (GitHubRelease, error) {
	// GitHub repository details
	repoOwner := "keploy"
	repoName := "keploy"

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", repoOwner, repoName)

	client := http.Client{
		Timeout: 4 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return GitHubRelease{}, err
	}

	resp, err := client.Do(req)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return GitHubRelease{}, ErrGitHubAPIUnresponsive
		}
		return GitHubRelease{}, err
	}
	defer resp.Body.Close()

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return GitHubRelease{}, err
	}
	return release, nil
}

// It checks if the cli is related to docker or not, it also returns if it is a docker compose file
func FindDockerCmd(cmd string) CmdType {
	// Convert command to lowercase for case-insensitive comparison
	cmdLower := strings.TrimSpace(strings.ToLower(cmd))

	// Define patterns for Docker and Docker Compose
	dockerPatterns := []string{"docker", "sudo docker"}
	dockerComposePatterns := []string{"docker-compose", "sudo docker-compose", "docker compose", "sudo docker compose"}

	// Check for Docker Compose command patterns and file extensions
	for _, pattern := range dockerComposePatterns {
		if strings.HasPrefix(cmdLower, pattern) {
			return DockerCompose
		}
	}
	// Check for Docker command patterns
	for _, pattern := range dockerPatterns {
		if strings.HasPrefix(cmdLower, pattern) {
			return Docker
		}
	}
	return Native
}

type CmdType string

const (
	Docker        CmdType = "docker"
	DockerCompose CmdType = "docker-compose"
	Native        CmdType = "native"
)

type RecordFlags struct {
	Path             string
	Command          string
	ContainerName    string
	Proxyport        uint32
	NetworkName      string
	Delay            uint64
	BuildDelay       time.Duration
	PassThroughPorts []uint
	ConfigPath       string
	EnableTele       bool
}

type TestFlags struct {
	Path               string
	Proxyport          uint32
	Command            string
	Testsets           []string
	ContainerName      string
	NetworkName        string
	Delay              uint64
	BuildDelay         time.Duration
	ApiTimeout         uint64
	PassThroughPorts   []uint
	ConfigPath         string
	MongoPassword      string
	CoverageReportPath string
	EnableTele         bool
	WithCoverage       bool
}

func getAlias(ctx context.Context, logger *zap.Logger) (string, error) {
	// Get the name of the operating system.
	osName := runtime.GOOS
	//TODO: configure the hardcoded port mapping
	switch osName {
	case "linux":
		alias := "sudo docker run --pull always --name keploy-v2 -e BINARY_TO_DOCKER=true -p 16789:16789 --privileged --pid=host -it -v " + os.Getenv("PWD") + ":" + os.Getenv("PWD") + " -w " + os.Getenv("PWD") + " -v /sys/fs/cgroup:/sys/fs/cgroup -v /sys/kernel/debug:/sys/kernel/debug -v /sys/fs/bpf:/sys/fs/bpf -v /var/run/docker.sock:/var/run/docker.sock -v " + os.Getenv("HOME") + "/.keploy-config:/root/.keploy-config -v " + os.Getenv("HOME") + "/.keploy:/root/.keploy --rm ghcr.io/keploy/keploy "
		return alias, nil
	case "darwin":
		cmd := exec.CommandContext(ctx, "docker", "context", "ls", "--format", "{{.Name}}\t{{.Current}}")
		out, err := cmd.Output()
		if err != nil {
			logger.Error("Failed to get the current docker context", zap.Error(err))
			return "", errors.New("failed to get alias")
		}
		dockerContext := strings.Split(strings.TrimSpace(string(out)), "\n")[0]
		if len(dockerContext) == 0 {
			logger.Error("Could not get the current docker context")
			return "", errors.New("failed to get alias")
		}
		dockerContext = strings.Split(dockerContext, "\n")[0]
		if dockerContext == "colima" {
			logger.Info("Starting keploy in docker with colima context, as that is the current context.")
			alias := "docker run --pull always --name keploy-v2 -e BINARY_TO_DOCKER=true -p 16789:16789 --privileged --pid=host -it -v " + os.Getenv("PWD") + ":" + os.Getenv("PWD") + " -w " + os.Getenv("PWD") + " -v /sys/fs/cgroup:/sys/fs/cgroup -v /sys/kernel/debug:/sys/kernel/debug -v /sys/fs/bpf:/sys/fs/bpf -v /var/run/docker.sock:/var/run/docker.sock -v " + os.Getenv("HOME") + "/.keploy-config:/root/.keploy-config -v " + os.Getenv("HOME") + "/.keploy:/root/.keploy --rm ghcr.io/keploy/keploy "
			return alias, nil
		} else {
			logger.Info("Starting keploy in docker with default context, as that is the current context.")
			alias := "docker run --pull always --name keploy-v2 -e BINARY_TO_DOCKER=true -p 16789:16789 --privileged --pid=host -it -v " + os.Getenv("PWD") + ":" + os.Getenv("PWD") + " -w " + os.Getenv("PWD") + " -v /sys/fs/cgroup:/sys/fs/cgroup -v debugfs:/sys/kernel/debug:rw -v /sys/fs/bpf:/sys/fs/bpf -v /var/run/docker.sock:/var/run/docker.sock -v " + os.Getenv("HOME") + "/.keploy-config:/root/.keploy-config -v " + os.Getenv("HOME") + "/.keploy:/root/.keploy --rm ghcr.io/keploy/keploy "
			return alias, nil
		}
	case "Windows":
		logger.Error("Windows is not supported. Use WSL2 instead.")
		return "", errors.New("failed to get alias")
	}
	return "", errors.New("failed to get alias")
}

//func appendFlags(flagName string, flagValue string) string {
//	if len(flagValue) > 0 {
//		// Check for = in the flagName.
//		if strings.Contains(flagName, "=") {
//			return " --" + flagName + flagValue
//		}
//		return " --" + flagName + " " + flagValue
//	}
//	return ""
//}

func RunInDocker(ctx context.Context, logger *zap.Logger, command string) error {
	//Get the correct keploy alias.
	keployAlias, err := getAlias(ctx, logger)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", keployAlias+" "+command)
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	logger.Debug("This is the keploy alias", zap.String("keployAlias:", keployAlias))
	err = cmd.Run()
	if err != nil {
		logger.Error("Failed to start keploy in docker", zap.Error(err))
		return err
	}
	return nil
}

// Keys returns an array containing the keys of the given map.
func Keys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func SentryInit(logger *zap.Logger, dsn string) {
	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		TracesSampleRate: 1.0,
	})
	if err != nil {
		logger.Debug("Could not initialise sentry.", zap.Error(err))
	}
}

func FetchHomeDirectory(isNewConfigPath bool) string {
	var configFolder = "/.keploy-config"

	if isNewConfigPath {
		configFolder = "/.keploy"
	}
	if runtime.GOOS == "windows" {
		home := os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
		if home == "" {
			home = os.Getenv("USERPROFILE")
		}
		return home + configFolder
	}

	return os.Getenv("HOME") + configFolder
}
