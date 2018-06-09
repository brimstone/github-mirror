package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/go-github/github"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"golang.org/x/oauth2"
	git "gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
	"gopkg.in/src-d/go-git.v4/plumbing/transport/http"
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

func openRepo(repoName string) (repo *git.Repository, err error) {
	repo, err = git.PlainOpen(basePath + "/" + repoName)
	if err == nil {
		return
	}
	defer profile(2, time.Now(), " Cloned %s", repoName)
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
		logit(0, "Error running command %s, %s\n", match, err)
	}

	return
}

func getHashRefs(repo *git.Repository) (m HashRefs, err error) {
	m = make(HashRefs)
	refs, _ := repo.References()
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
				Username: "brimstone",
				Password: viper.GetString("token"),
			},
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

func errExit(err error, msg string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, msg, err)
		os.Exit(1)
	}
}

func updateRepo(repoName string) {
	logit(2, "Starting %s\n", repoName)
	repo, err := openRepo(repoName)
	if err != nil {
		log.Printf("Error opening repo: %s\n", err)
		return
	}

	before, err := getHashRefs(repo)
	errExit(err, "Error getting ref hashes: %s\n")
	err = fetchRemotes(repo)
	if err != nil {
		log.Printf("Error fetching repo: %s\n", err)
		return
	}
	after, err := getHashRefs(repo)
	errExit(err, "Error getting ref hashes: %s\n")

	d, err := diffHashRefs(before, after)
	errExit(err, "Error getting differences of hashrefs: %s\n")
	//log.Printf("Diff: %#v\n", d)

	for k, v := range d {
		match := ""
		switch v {
		case Changed:
			logit(1, "%s Ref %s changed\n", repoName, k)
			match = findMatch(repoName, viper.GetStringMapString("changed"))
		case Added:
			logit(1, "%s Ref %s added\n", repoName, k)
			match = findMatch(repoName, viper.GetStringMapString("added"))
		case Removed:
			logit(1, "%s Ref %s removed\n", repoName, k)
			match = findMatch(repoName, viper.GetStringMapString("removed"))
		}
		if match == "" {
			continue
		}
		err := executeCmd(repoName, k, match)
		if err != nil {
			logit(0, "Error running command %s, %s\n", match, err)
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

func profile(level int, start time.Time, format string, args ...interface{}) {
	var b []interface{}
	b = append(b, time.Now().Sub(start))
	b = append(b, args...)
	logit(level, "Duration: %s"+format+"\n", b...)
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
			allRepos = append(allRepos, *repo.Repository.Owner.Login+"/"+*repo.Repository.Name)
		}
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage

	}
	return
}

func logit(level int, format string, args ...interface{}) {
	if viper.GetInt("loglevel") >= level {
		log.Printf(format, args...)
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
	flag.Int("loglevel", 1, "Log level, 0 is silent, 3 is verbose")
	flag.Int("workers", 5, "number of workers")

	// setup configs
	viper.SetConfigName("config")
	viper.AddConfigPath("/")
	viper.AddConfigPath("$HOME/.github-mirror")
	viper.AddConfigPath(".")
	err := viper.ReadInConfig()
	if err != nil {
		logit(0, "Error reading config: %s\n", err)
	}
	viper.WatchConfig()
	viper.OnConfigChange(func(e fsnotify.Event) {
		logit(2, "Config file changed:", e.Name)
	})
	viper.AutomaticEnv()
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()
	viper.BindPFlags(pflag.CommandLine)

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
	defer profile(1, time.Now(), "")

	logit(2, "Getting self repos\n")
	repoMap := make(map[string]bool)
	selfRepos, err := getSelfRepos(client)
	errExit(err, "Unable to get public repos: %s\n")
	for _, repo := range selfRepos {
		repoMap[repo] = true
	}

	logit(2, "Getting starred repos\n")
	starredRepos, err := getStarredRepos(client)
	errExit(err, "Unable to get starred repos: %s\n")
	for _, repo := range starredRepos {
		repoMap[repo] = true
	}

	logit(2, "Self Repos: %d, Starred Repos: %d", len(selfRepos), len(starredRepos))

	repoChan := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < viper.GetInt("workers"); i++ {
		go func() {
			wg.Add(1)
			defer wg.Done()
			for {
				repo, ok := <-repoChan
				if !ok {
					break
				}
				updateRepo(repo)
			}
		}()
	}
	for repo := range repoMap {
		logit(2, "Adding %s\n", repo)
		repoChan <- repo
	}
	close(repoChan)
	logit(2, "Waiting for everything to finish\n")
	wg.Wait()
}
