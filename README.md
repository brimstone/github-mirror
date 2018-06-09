github-mirror
=============

Mirror github repos locally. Also, add hooks to any github repo you've starred.


Usage
=====

Basic, simple usage is just setting the TOKEN environment variable to a personal
github token and running the program:

```
TOKEN=12345abc ./github-mirror
```

Advanced usage is specifying a config file in YAML, JSON, or TOML format and
calling github mirror without arguments. An example config file is in
`config.example.yaml`.


Configuration
=============
- `token`: This is a personal token from github
- `loglevel`: This is how much to log to stdout. 0 means only errors, 2 means a
  lot of debug information.
- `basepath`: This is the root directory to mirror repos.
- `added`, `changed`, `removed`: These are a map of keys being partial or
  complete repo names and values being the command to run when that repo
  changes. The longest prefix has priority. In the example config file, any of
  the repos for the brimstone user will cause the `./build` script to be run,
  and any other repo will cause the `./notify` script to be run. Environment
  variables `REPO` and `BRANCH` are set.
- `added`: This happens with a new repo or branch is found starred or under the
  user's control. When there's a new repo, the `BRANCH` variable is set, but
  empty.
- `changed`: This happens when a branch to a repo points to a different
  reference than before.
- `removed`: This happens when a branch in a repo is removed.


TODO
====
- Make removed trigger when a repo is unstarred or removed.
- Make changed and added also have a REF variable pointing to the gitish.
  Changed might want some sort of OLD_REF as well.
- Support a url instead of a command, detect with the `http` prefix. The format
  for this POST should be the same as the github webhook.
- Make the map for added, changed and removed support a single key string as
  well as an array of commands or urls.
