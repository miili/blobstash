package config // import "a4.io/blobstash/pkg/config"

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/inconshreveable/log15"
	"gopkg.in/yaml.v2"

	"a4.io/blobstash/pkg/config/pathutil"
)

var (
	DefaultListen  = ":8051"
	LetsEncryptDir = "letsencrypt"
)

// AppConfig holds an app configuration items
type AppConfig struct {
	Name       string `yaml:"name"`
	Path       string `yaml:"path"` // App path, optional?
	Entrypoint string `yaml:"entrypoint"`
	Domain     string `yaml:"domain"`
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`
	Proxy      string `yaml:"proxy"`

	Config map[string]interface{} `yaml:"config"`
}

type S3Repl struct {
	Bucket  string `yaml:"bucket"`
	Region  string `yaml:"region"`
	KeyFile string `yaml:"key_file"`
}

type Replication struct {
	EnableOplog bool `yaml:"enable_oplog"`
}

type ReplicateFrom struct {
	URL    string `yaml:"url"`
	APIKey string `yaml:"api_key"`
}

func (s3 *S3Repl) Key() (*[32]byte, error) {
	if s3.KeyFile == "" {
		return nil, nil
	}
	var out [32]byte
	data, err := ioutil.ReadFile(s3.KeyFile)
	if err != nil {
		return nil, err
	}
	copy(out[:], data)
	return &out, nil
}

// Config holds the configuration items
type Config struct {
	init     bool
	Listen   string `yaml:"listen"`
	LogLevel string `yaml:"log_level"`
	// TLS     bool     `yaml:"tls"`
	AutoTLS bool     `yaml:"tls_auto"`
	Domains []string `yaml:"tls_domains"`

	APIKey     string  `yaml:"api_key"`
	SharingKey string  `yaml:"sharing_key"`
	DataDir    string  `yaml:"data_dir"`
	S3Repl     *S3Repl `yaml:"s3_replication"`

	Apps          []*AppConfig    `yaml:"apps"`
	Docstore      *DocstoreConfig `yaml:"docstore"`
	Replication   *Replication    `yaml:"replication"`
	ReplicateFrom *ReplicateFrom  `yaml:"replicate_from"`

	// Items defined with the CLI flags
	ScanMode      bool `yaml:"-"`
	S3ScanMode    bool `yaml:"-"`
	S3RestoreMode bool `yaml:"-"`
}

func (c *Config) LogLvl() log15.Lvl {
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	lvl, err := log15.LvlFromString(c.LogLevel)
	if err != nil {
		panic(err)
	}
	return lvl
}

type DocstoreConfig struct {
	StoredQueries []*StoredQuery `yaml:"stored_queries"`
}

type StoredQuery struct {
	Name string `yaml:"name"`
	Path string `yaml:"path"`
}

// New initialize a config object by loading the YAML path at the given path
func New(path string) (*Config, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	conf := &Config{}
	if err := yaml.Unmarshal([]byte(data), &conf); err != nil {
		return nil, err
	}
	return conf, nil
}

// VarDir returns the directory where the index will be stored
func (c *Config) ConfigDir() string {
	// TODO(tsileo): allow override?
	return pathutil.ConfigDir()
}

// VarDir returns the directory where the index will be stored
func (c *Config) VarDir() string {
	if c.DataDir != "" {
		return c.DataDir
	}
	return pathutil.VarDir()
}

// Init initialize the config.
//
// It will try to create all the needed directory.
func (c *Config) Init() error {
	if c.init {
		return nil
	}
	if _, err := os.Stat(c.VarDir()); os.IsNotExist(err) {
		if err := os.MkdirAll(c.VarDir(), 0700); err != nil {
			return err
		}
	}
	if _, err := os.Stat(c.ConfigDir()); os.IsNotExist(err) {
		if err := os.MkdirAll(c.ConfigDir(), 0700); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(c.ConfigDir(), LetsEncryptDir)); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Join(c.ConfigDir(), LetsEncryptDir), 0700); err != nil {
			return err
		}
	}
	if c.SharingKey == "" {
		return fmt.Errorf("missing `sharing_key` config item")
	}
	if c.S3Repl != nil {
		// Set default region
		if c.S3Repl.Region == "" {
			c.S3Repl.Region = "us-east-1"
		}
	}
	c.init = true
	return nil
}

// Sync url config parsing
//u, err := url.Parse("http://:123@127.0.0.1:8053")
//	if err != nil {
//		log.Fatal(err)
//	}
//	u.User = nil
//	//apiKey, _ := u.User.Password()
//	fmt.Printf("%+v", u)
