package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blang/semver"
	"github.com/brimstone/github-mirror/pkg/version"
	"github.com/fsnotify/fsnotify"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/google/go-github/github"
	"github.com/rhysd/go-github-selfupdate/selfupdate"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"golang.org/x/oauth2"
)

type HashRefs map[plumbing.ReferenceName]plumbing.Hash

type ChangeType uint8

const (
	Added = ChangeType(iota)
	Removed
	Changed
)

type HashRefsDiff map[plumbing.ReferenceName]ChangeType

var basePath = ""

func openRepo(logger *log.Logger, repoName string) (repo *git.Repository, err error) {
	repo, err = git.PlainOpen(basePath + "/" + repoName)
	if err == nil {
		return
	}
	defer profile(logger, 2, time.Now(), " Cloned %s", repoName)
	opt := &git.CloneOptions{
		URL:        "https://github.com/" + repoName,
		NoCheckout: true,
		Auth: &http.BasicAuth{
			Username: viper.GetString("username"),
			Password: viper.GetString("token"),
		},
	}
	if viper.GetInt("loglevel") >= 2 {
		opt.Progress = os.Stdout
	}
	repo, err = git.PlainClone(basePath+"/"+repoName, false, opt)
	if err != nil {
		return
	}
	match := findMatch(repoName, viper.GetStringMapString("added"))
	err = executeCmd(repoName, plumbing.ReferenceName(""), match)
	if err != nil {
		logit(logger, 0, "Error running command %s, %s\n", match, err)
	}

	return
}

func getHashRefs(logger *log.Logger, repo *git.Repository) (m HashRefs, err error) {
	m = make(HashRefs)
	refs, err := repo.References()
	if err != nil {
		logit(logger, 2, "Delaying before trying to get refs again")
		time.Sleep(time.Second)
		return getHashRefs(logger, repo)
	}
	refs.ForEach(func(ref *plumbing.Reference) error {
		m[ref.Name()] = ref.Hash()
		return nil
	})

	return
}

func fetchRemotes(repo *git.Repository) (err error) {
	remotes, _ := repo.Remotes()
	for _, remote := range remotes {
		opt := &git.FetchOptions{
			Auth: &http.BasicAuth{
				Username: viper.GetString("username"),
				Password: viper.GetString("token"),
			},
			Tags: git.AllTags,
		}
		if viper.GetInt("loglevel") >= 2 {
			opt.Progress = os.Stdout
		}
		err = remote.Fetch(opt)
		if err == git.NoErrAlreadyUpToDate {
			err = nil
		}
		if err != nil {
			return
		}
	}
	return
}

func diffHashRefs(before HashRefs, after HashRefs) (d HashRefsDiff, err error) {
	d = make(HashRefsDiff)
	for k, v := range before {
		if v != after[k] {
			d[k] = Changed
		}
		delete(before, k)
		delete(after, k)
	}
	for k := range after {
		d[k] = Added
	}
	for k := range before {
		d[k] = Removed
	}
	return
}

func errExit(logger *log.Logger, err error, msg string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, msg, err)
		os.Exit(1)
	}
}

func updateRepo(repoName string) {
	logger := log.New(os.Stderr, "["+repoName+"] ", log.LstdFlags)
	logit(logger, 2, "Starting %s\n", repoName)
	repo, err := openRepo(logger, repoName)
	if err != nil {
		if err.Error() == "remote repository is empty" {
			return
		}
		logger.Printf("Error opening repo: %s\n", err)
		return
	}

	before, err := getHashRefs(logger, repo)
	errExit(logger, err, "Error getting ref hashes: %s\n")
	err = fetchRemotes(repo)
	if err != nil {
		// Hide empty repo errors
		if err.Error() == "remote repository is empty" {
			logit(logger, 1, "Error fetching repo: %s\n", err)
			return
		}
		if err.Error() == "reference has changed concurrently" { // TODO: try a few times and don't just ignore this
			return
		}
		logit(logger, 0, "Error fetching repo: %s\n", err)
		return
	}
	after, err := getHashRefs(logger, repo)
	errExit(logger, err, "Error getting ref hashes: %s\n")

	d, err := diffHashRefs(before, after)
	errExit(logger, err, "Error getting differences of hashrefs: %s\n")

	for k, v := range d {
		match := ""
		switch v {
		case Changed:
			logit(logger, 1, "%s Ref %s changed\n", repoName, k)
			match = findMatch(repoName, viper.GetStringMapString("changed"))
		case Added:
			logit(logger, 1, "%s Ref %s added\n", repoName, k)
			match = findMatch(repoName, viper.GetStringMapString("added"))
		case Removed:
			logit(logger, 1, "%s Ref %s removed\n", repoName, k)
			match = findMatch(repoName, viper.GetStringMapString("removed"))
		}
		if match == "" {
			continue
		}
		err := executeCmd(repoName, k, match)
		if err != nil {
			logit(logger, 0, "Error running command %s, %s\n", match, err)
		}
	}
}

func executeCmd(repoName string, branchName plumbing.ReferenceName, command string) (err error) {
	cmd := exec.Command(command)
	branch := string(branchName)
	branch = strings.TrimPrefix(branch, "refs/remotes/")
	branch = strings.TrimPrefix(branch, "refs/")
	cmd.Env = append(os.Environ(),
		"REPO="+repoName,
		"BRANCH="+string(branchName),
	)
	err = cmd.Run()
	return
}

func profile(logger *log.Logger, level int, start time.Time, format string, args ...interface{}) {
	var b []interface{}
	b = append(b, time.Now().Sub(start))
	b = append(b, args...)
	logit(logger, level, "Duration: %s"+format+"\n", b...)
}

func getSelfRepos(client *github.Client) (allRepos []string, err error) {
	opt := &github.RepositoryListOptions{
		ListOptions: github.ListOptions{PerPage: 500},
	}
	var repos []*github.Repository
	var resp *github.Response
	for {
		repos, resp, err = client.Repositories.List(context.Background(), "", opt)
		if err != nil {
			return
		}
		for _, repo := range repos {
			allRepos = append(allRepos, *repo.Owner.Login+"/"+*repo.Name)
		}
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage

	}
	return
}

func getStarredRepos(client *github.Client) (allRepos []string, err error) {
	opt := &github.ActivityListStarredOptions{
		ListOptions: github.ListOptions{PerPage: 500},
	}
	var repos []*github.StarredRepository
	var resp *github.Response
	for {
		repos, resp, err = client.Activity.ListStarred(context.Background(), "", opt)
		if err != nil {
			return
		}
		for _, repo := range repos {
			if repo.Repository.Owner == nil {
				//fmt.Println("Owner is nil: " + *repo.Repository.URL)
				continue
			}
			allRepos = append(allRepos, *repo.Repository.Owner.Login+"/"+*repo.Repository.Name)
		}
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage

	}
	return
}

func logit(logger *log.Logger, level int, format string, args ...interface{}) {
	if viper.GetInt("loglevel") >= level {
		logger.Printf(format, args...)
	}
}

func findMatch(needle string, haystack map[string]string) (result string) {
	matchLength := -1
	for k, v := range haystack {
		if strings.HasPrefix(needle, k) && len(k) > matchLength {
			result = v
		}
	}
	return
}

func main() {

	flag.String("token", "", "github token")
	flag.Int("loglevel", 1, "log level, 0 is silent, 3 is verbose")
	flag.Int("workers", 5, "number of workers")
	flag.Bool("auto-upgrade", false, "automatically upgrade if a new version is available")

	// setup configs
	viper.SetConfigName("config")
	viper.AddConfigPath("/")
	viper.AddConfigPath("$HOME/.github-mirror")
	viper.AddConfigPath(".")
	err := viper.ReadInConfig()
	logger := log.New(os.Stderr, "[main] ", log.LstdFlags)
	if err != nil {
		logit(logger, 0, "Error reading config: %s", err)
	}
	viper.WatchConfig()
	viper.OnConfigChange(func(e fsnotify.Event) {
		logit(logger, 2, "Config file changed: %s", e.Name)
	})
	viper.AutomaticEnv()
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()
	viper.BindPFlags(pflag.CommandLine)

	// Figure out the current version
	v := semver.MustParse(version.Version)
	if viper.GetBool("auto-upgrade") {
		// Check for updates
		pubkey, err := version.PublicKey()
		if err != nil {
			logit(logger, 0, "Error occurred while extracting public key: %s", err)
			return
		}
		up, err := selfupdate.NewUpdater(selfupdate.Config{
			Validator: &selfupdate.ECDSAValidator{
				PublicKey: pubkey,
			},
			Filters: []string{
				version.Binary,
			},
		})
		latest, err := up.UpdateSelf(v, "brimstone/github-mirror")
		if err != nil {
			logit(logger, 0, "Binary update failed: %s", err)
			return
		}
		if latest.Version.Equals(v) {
			// latest version is the same as current version. It means current binary is up to date.
			logit(logger, 1, "Current binary is the latest version %s", v)
		} else {
			logit(logger, 1, fmt.Sprintf("Successfully updated to version %s", latest.Version))
			me, _ := os.Executable()
			syscall.Exec(me, os.Args, os.Environ())
		}

	} else {
		// Check for updates

		latest, found, err := selfupdate.DetectLatest("brimstone/github-mirror")
		if err != nil {
			logit(logger, 0, "Error occurred while detecting version: %s", err)
		}

		if found && latest.Version.GT(v) {
			logit(logger, 0, "New version available")
		}
	}

	if viper.GetString("token") == "" {
		fmt.Fprintf(os.Stderr, "Token must be set")
		os.Exit(1)
	}

	basePath = viper.GetString("basepath")

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: viper.GetString("token")},
	)
	tc := oauth2.NewClient(context.Background(), ts)
	client := github.NewClient(tc)
	defer profile(logger, 1, time.Now(), "")

	logit(logger, 2, "Getting self repos\n")
	repoMap := make(map[string]bool)
	selfRepos, err := getSelfRepos(client)
	errExit(logger, err, "Unable to get public repos: %s\n")
	for _, repo := range selfRepos {
		logit(logger, 3, "Repo: %s\n", repo)
		repoMap[repo] = true
	}

	logit(logger, 2, "Getting starred repos\n")
	starredRepos, err := getStarredRepos(client)
	errExit(logger, err, "Unable to get starred repos: %s\n")
	for _, repo := range starredRepos {
		repoMap[repo] = true
	}

	logit(logger, 2, "Self Repos: %d, Starred Repos: %d", len(selfRepos), len(starredRepos))

	repoChan := make(chan string)
	var wg sync.WaitGroup
	ignore := viper.GetStringSlice("ignore")
	for i := 0; i < viper.GetInt("workers"); i++ {
		go func() {
			wg.Add(1)
			defer wg.Done()
			for {
				repo, ok := <-repoChan
				if !ok {
					break
				}
				if slices.Contains(ignore, repo) {
					logit(logger, 2, "Ignoring repo: %s", repo)
					continue
				}
				updateRepo(repo)
			}
		}()
	}
	for repo := range repoMap {
		logit(logger, 2, "Adding %s\n", repo)
		repoChan <- repo
	}
	close(repoChan)
	logit(logger, 2, "Waiting for everything to finish\n")
	wg.Wait()
}
