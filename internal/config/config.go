package config

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"syscall"

	"github.com/spf13/viper"
	gitlab "github.com/xanzy/go-gitlab"
	"github.com/zaquestion/lab/internal/git"
	"golang.org/x/crypto/ssh/terminal"
)

const defaultGitLabHost = "https://gitlab.com"

// New prompts the user for the default config values to use with lab, and save
// them to the provided confpath (default: ~/.config/lab.hcl)
func New(confpath string, r io.Reader) error {
	var (
		reader      = bufio.NewReader(r)
		host, token string
		err         error
	)
	// If core host is set in the environment (LAB_CORE_HOST) we only want
	// to prompt for the token. We'll use the environments host and place
	// it in the config. In the event both the host and token are in the
	// env, this function shouldn't be called in the first place
	if viper.GetString("core.host") == "" {
		fmt.Printf("Enter GitLab host (default: %s): ", defaultGitLabHost)
		host, err = reader.ReadString('\n')
		host = strings.TrimSpace(host)
		if err != nil {
			return err
		}
		if host == "" {
			host = defaultGitLabHost
		}
	} else {
		// Required to correctly write config
		host = viper.GetString("core.host")
	}

	tokenURL, err := url.Parse(host)
	if err != nil {
		return err
	}
	tokenURL.Path = "profile/personal_access_tokens"

	fmt.Printf("Create a token here: %s\nEnter default GitLab token (scope: api): ", tokenURL.String())
	token, err = readPassword()
	if err != nil {
		return err
	}

	viper.Set("core.host", host)
	viper.Set("core.token", token)
	if err := viper.WriteConfigAs(confpath); err != nil {
		return err
	}
	fmt.Printf("\nConfig saved to %s\n", confpath)
	return nil
}

var readPassword = func() (string, error) {
	byteToken, err := terminal.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(byteToken)), nil
}

// CI returns credentials suitable for use within GitLab CI or empty strings if
// none found.
func CI() (string, string, string) {
	ciToken := os.Getenv("CI_JOB_TOKEN")
	if ciToken == "" {
		return "", "", ""
	}
	ciHost := strings.TrimSuffix(os.Getenv("CI_PROJECT_URL"), os.Getenv("CI_PROJECT_PATH"))
	if ciHost == "" {
		return "", "", ""
	}
	ciUser := os.Getenv("GITLAB_USER_LOGIN")

	return ciHost, ciUser, ciToken
}

// ConvertHCLtoTOML() converts an .hcl file to a .toml file
func ConvertHCLtoTOML(oldpath string, newpath string, file string) {
	oldconfig := oldpath + "/" + file + ".hcl"
	newconfig := newpath + "/" + file + ".toml"

	if _, err := os.Stat(oldconfig); os.IsNotExist(err) {
		return
	}

	if _, err := os.Stat(newconfig); err == nil {
		return
	}

	// read in the old config HCL file and write out the new TOML file
	viper.Reset()
	viper.SetConfigName("lab")
	viper.SetConfigType("hcl")
	viper.AddConfigPath(oldpath)
	viper.ReadInConfig()
	viper.SetConfigType("toml")
	viper.WriteConfigAs(newconfig)

	// delete the old config HCL file
	if err := os.Remove(oldconfig); err != nil {
		fmt.Println("Warning: Could not delete old config file", oldconfig)
	}

	// HACK
	// viper HCL parsing is broken and simply translating it to a TOML file
	// results in a broken toml file.  The issue is that there are double
	// square brackets for each entry where there should be single
	// brackets.  Note: this hack only works because the config file is
	// simple and doesn't contain deeply embedded config entries.
	text, err := ioutil.ReadFile(newconfig)
	if err != nil {
		log.Fatal(err)
	}

	text = bytes.Replace(text, []byte("[["), []byte("["), -1)
	text = bytes.Replace(text, []byte("]]"), []byte("]"), -1)

	if err = ioutil.WriteFile(newconfig, text, 0666); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	// END HACK

	fmt.Println("INFO: Converted old config", oldconfig, "to new config", newconfig)
}

func getUser(host, token string, skipVerify bool) string {
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: skipVerify,
			},
		},
	}
	lab, _ := gitlab.NewClient(token, gitlab.WithHTTPClient(httpClient), gitlab.WithBaseURL(host+"/api/v4"))
	u, _, err := lab.Users.CurrentUser()
	if err != nil {
		log.Fatal(err)
	}
	return u.Username
}

// LoadConfig() loads the main config file and returns a tuple of
//  host, user, token, ca_file, skipVerify
func LoadConfig() (string, string, string, string, bool) {

	// Attempt to auto-configure for GitLab CI.
	// Always do this before reading in the config file o/w CI will end up
	// with the wrong data.
	host, user, token := CI()
	if host != "" && user != "" && token != "" {
		return host, user, token, "", false
	}

	// Try to find XDG_CONFIG_HOME which is declared in XDG base directory
	// specification and use it's location as the config directory
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	confpath := os.Getenv("XDG_CONFIG_HOME")
	if confpath == "" {
		confpath = path.Join(home, ".config")
	}
	labconfpath := confpath + "/lab"
	if _, err := os.Stat(labconfpath); os.IsNotExist(err) {
		os.MkdirAll(labconfpath, 0700)
	}

	// Convert old hcl files to toml format.
	// NO NEW FILES SHOULD BE ADDED BELOW.
	ConvertHCLtoTOML(confpath, labconfpath, "lab")
	ConvertHCLtoTOML(".", ".", "lab")
	var labgitDir string
	gitDir, err := git.GitDir()
	if err == nil {
		labgitDir = gitDir + "/lab"
		ConvertHCLtoTOML(gitDir, labgitDir, "lab")
		ConvertHCLtoTOML(labgitDir, labgitDir, "show_metadata")
	}

	viper.SetConfigName("lab")
	viper.SetConfigType("toml")
	viper.AddConfigPath(".")
	viper.AddConfigPath(labconfpath)
	if labgitDir != "" {
		viper.AddConfigPath(labgitDir)
	}

	viper.SetEnvPrefix("LAB")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	if _, ok := viper.ReadInConfig().(viper.ConfigFileNotFoundError); ok {
		err := New(path.Join(labconfpath, "lab.toml"), os.Stdin)
		if err != nil {
			log.Fatal(err)
		}

		err = viper.ReadInConfig()
		if err != nil {
			log.Fatal(err)
		}
	}

	host = viper.GetString("core.host")
	user = viper.GetString("core.user")
	token = viper.GetString("core.token")
	tlsSkipVerify := viper.GetBool("tls.skip_verify")
	ca_file := viper.GetString("tls.ca_file")

	if user == "" {
		user = getUser(host, token, tlsSkipVerify)
		if strings.TrimSpace(os.Getenv("LAB_CORE_TOKEN")) == "" && strings.TrimSpace(os.Getenv("LAB_CORE_HOST")) == "" {
			viper.Set("core.user", user)
			viper.WriteConfig()
		}
	}

	return host, user, token, ca_file, tlsSkipVerify
}
