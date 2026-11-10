# kc-env

English | [日本語](README.ja.md)

Keep API keys and tokens out of your shell config and `.env` files. `kc-env` stores secrets in **profiles**, encrypts them with a key that lives in the Mac's **Secure Enclave**, and asks for **Touch ID** (or your login password) every time a secret is decrypted. It works fully offline.

```sh
kc-env set work GITHUB_TOKEN              # hidden prompt; stored encrypted
kc-env set personal GITHUB_TOKEN          # same key name, different value, different profile
kc-env exec work -- gh repo list          # Touch ID once; the secret is passed only to `gh`
```

## Features

- The same variable name (e.g. `GITHUB_TOKEN`) can hold a different value in each profile.
- The decryption key is hardware-bound: it is generated inside the Secure Enclave and cannot be exported. Copies of the vault are useless on any other Mac.
- Every decryption asks for Touch ID, enforced by the Secure Enclave itself (password fallback).
- No plaintext is written to disk: values stay in memory and reach a command only through its environment.
- Sessions (optional) keep selected profiles in memory for a limited time, so a burst of work needs Touch ID only once. Only programs started inside the session can use them.
- Deleting always needs Touch ID, even inside a session.
- It works with any tool: `exec` passes secrets as environment variables and `header` prints HTTP headers as JSON. kc-env has no tool-specific integration; [examples](#using-kc-env-with-other-tools-examples) show how to combine it with other tools, AI coding assistants included.
- Messages are in English or Japanese, selected from `KC_ENV_LANG` / `LC_ALL` / `LC_MESSAGES` / `LANG`.
- It is offline and small: one Go binary that links the [age](https://github.com/FiloSottile/age) library, plus [age-plugin-se](https://github.com/remko/age-plugin-se) for the Secure Enclave. No network access at runtime.

How the encryption works is described in [docs/encryption.md](docs/encryption.md).

## Requirements

- A Mac with a Secure Enclave (Apple silicon, or an Intel Mac with a T2 chip). Touch ID is recommended; without it, your login password is used.
- zsh, for Tab completion (optional).
- To build: git, Go 1.26 or later, and Swift 6.2 or later (Xcode or the Command Line Tools).

## Installation

Get the source:

```sh
git clone https://github.com/koukichi-net/se-vault.git
cd se-vault
```

There is no build script. Build the two programs with the handful of commands in [docs/build.md](docs/build.md) (kc-env with `go build` after verifying every Go module against `go.sum`; age-plugin-se from a pinned, verified commit with `swiftc`), then install them:

```sh
# Install to a directory of your choice (the "home"). ~/.local/share/kc-env is a good default:
# it is outside the folders covered by macOS privacy prompts and outside iCloud Drive.
mkdir -p ~/.local/share/kc-env ~/.local/bin
chmod 700 ~/.local/share/kc-env
cp -Rp bin completions ~/.local/share/kc-env/
ln -sf ~/.local/share/kc-env/bin/kc-env ~/.local/bin/kc-env   # ~/.local/bin must be on your PATH

kc-env init                   # create the Secure Enclave key (once)
```

The home is always "the directory above `bin/kc-env`", resolved through symlinks, so you can move it later without changing any configuration. Only the symlink needs to be updated. The home cannot be redirected through an environment variable, and kc-env stops if the home, the programs or the key file are writable by anyone but you. Keep the home's path at 77 characters or fewer: sessions use a Unix socket inside it, and macOS limits socket paths to 104 bytes.

### Shell setup (optional)

For Tab completion, add this to `~/.zshrc` **before** compinit (with oh-my-zsh: before `source $ZSH/oh-my-zsh.sh`):

```sh
fpath=("${${:-$HOME/.local/bin/kc-env}:A:h:h}/completions" $fpath)
```

## Usage

Run `kc-env help` for the full reference, including the paths in use.

### Storing and using secrets

```sh
kc-env set work API_TOKEN                 # hidden prompt, or from a pipe: pbpaste | kc-env set work API_TOKEN
kc-env list                               # all profiles and key names (no Touch ID, values are never shown)
kc-env ls work                            # key names of one profile
kc-env exec work -- ./deploy.sh           # run a command with the profile's keys as environment variables
kc-env get work API_TOKEN                 # print one value (avoid this where output is logged)
kc-env header work API_TOKEN              # {"Authorization":"Bearer <value>"} for HTTP headers
```

When there is no terminal (for example in tools that run commands without a TTY), `set` asks for the value in a hidden macOS dialog.

Key names must be valid environment variable names. Names that change how programs start (`PATH`, `HOME`, `DYLD_*`, `LD_*`, `NODE_OPTIONS`, ...) are refused. Profile names may contain letters, digits, `.`, `_` and `-`.

### Deleting

```sh
kc-env rm work API_TOKEN OTHER_KEY        # remove keys
kc-env rm --all work                      # remove every key, keep the empty profile (asks first)
kc-env rm-profile work                    # remove an empty profile; refuses while keys remain
kc-env rm-profile --force work            # remove a profile together with its keys (asks first)
```

Every one of these asks for Touch ID with the Secure Enclave key, even inside a session: a session lets programs read secrets, not delete them unattended.

### Sessions: Touch ID once for a while

```sh
kc-env session --only work --ttl 30m -- <command>   # default TTL 30m (at most 24h); command defaults to your shell
kc-env cache                              # this session: expiry, scope, cached profiles
kc-env cache list                         # every running session on this Mac
kc-env cache rm --current | --all | <ID>  # end sessions (Touch ID)
kc-env lock                               # drop this session's cache; the next use asks again
```

Inside a session, the first decryption of each allowed profile asks for Touch ID, and later ones are answered from memory until the session ends or expires. The `kc-env session` process keeps the decrypted profiles in its own memory, like ssh-agent: nothing is written to disk, no key is put in the environment, and it answers only processes that were started under `<command>`. It also enforces the TTL and `--only` itself, so a program inside the session cannot extend them.

**Every program started inside the session can read those secrets without asking you**, and so can anything such a program runs (see [the threat model](docs/encryption.md#8-threat-model)). Keep `--only` narrow, keep the TTL short, and `kc-env lock` when you finish.

### Wiping everything

```sh
kc-env destroy
```

This deletes the Secure Enclave key reference and every profile, and ends every session. The program itself stays, and `kc-env init` starts over. It runs only from an interactive terminal and only outside a session. It shows what will be lost, then asks for `y/N`, then for a typed confirmation phrase, then waits 10 seconds, and finally asks for Touch ID. **This cannot be undone.**

### Language

Messages, help and Tab completion follow the first non-empty of `KC_ENV_LANG`, `LC_ALL`, `LC_MESSAGES` and `LANG` (`C`/`POSIX` mean English). Unknown languages fall back to English.

```sh
KC_ENV_LANG=ja kc-env help
```

Translations live in [`cmd/kc-env/locales/`](cmd/kc-env/locales) as [go-i18n](https://github.com/nicksnyder/go-i18n) TOML files and are embedded in the binary. To add a language, copy `en.toml` to `<lang>.toml` (for example `de.toml`), translate the values, and run `go test ./...`. The tests fail if any message is missing, extra, or uses an unknown placeholder. Plural forms use go-i18n's CLDR categories (`one`, `other`, ...).

## Using kc-env with other tools (examples)

kc-env itself has no integration with any particular tool, AI assistants included. It offers generic building blocks — `exec`, `header` and `session` — and the snippets below show how to combine them. Copy and adapt them to your own configuration; they are not installed by kc-env.

### Shell shortcuts (`~/.zshrc`)

```sh
# Run any command with a profile's secrets:   kx work terraform plan
kx() { local p=$1; shift; kc-env exec "$p" -- "$@"; }

# Start a command in a session, so the profile asks for Touch ID only once:
#   ks work zsh
ks() { local p=$1; shift; kc-env session --only "$p" -- "$@"; }

# Example: an AI coding assistant (here Claude Code) inside a 30-minute session
# for the "work" profile. Extra arguments go to the tool: ccs --resume
alias ccs='kc-env session --only work --ttl 30m -- claude'
```

Inside a session, **every program started in it — an AI agent included — can use the cached secrets without asking you.** Keep `--only` to the profiles the task needs and the TTL short.

### Tools that read secrets from the environment

Wrap the command with `exec`. For example, a stdio MCP server configured in an AI assistant:

```json
"my-server": {
  "command": "/Users/<you>/.local/bin/kc-env",
  "args": ["exec", "work", "--", "npx", "-y", "some-mcp-server"]
}
```

### Tools that take HTTP headers from a command

`header` prints one header as JSON. For example, Claude Code's `headersHelper` for an HTTP MCP server:

```json
"my-server": {
  "type": "http",
  "url": "https://example.com/mcp",
  "headersHelper": "/Users/<you>/.local/bin/kc-env header work API_TOKEN"
}
```

Use `--name` and `--prefix` for other header formats, for example `kc-env header work API_TOKEN --name X-Api-Key --prefix ''`.

## Security notes

- Losing the Mac, or erasing it, means losing access to the vault. This is by design: there is no recovery key. Keep the ability to reissue your API keys.
- Key names are stored in plaintext (`vault/<profile>.keys`) so they can be listed without Touch ID. Values are always encrypted.
- Once a value is handed to a command, that command (and anything it starts) can read it. On macOS, other processes running as you can also read that command's environment (`ps -wwE`) while it runs, sessions included.
- When you move a secret into kc-env, remove its old `export` from your shell startup files, reissue it, and restart your terminals: running programs keep the environment they started with.
- Anything running as you can replace kc-env's own files or delete the vault. Touch ID stops silent decryption; it is not a sandbox.
- See [docs/encryption.md](docs/encryption.md) for the threat model and its limits.

## Files

```
<home>/
  bin/kc-env               the tool (Go binary)
  bin/age-plugin-se        Secure Enclave plugin
  completions/_kc-env      zsh completion
  identity.txt             reference to the Secure Enclave key (created by init)
  vault/<profile>.age      encrypted profile
  vault/<profile>.keys     key-name index (no values)
  run/<id>.sock            socket of a running session (no secrets on disk)
```

Repository layout: `cmd/kc-env/` (Go source, tests and `locales/`), `completions/`, `docs/`. `bin/`, keys and vault files are listed in `.gitignore`.

## Development

```sh
go test ./...     # unit tests, translation checks; no Secure Enclave needed (tests use software keys, which the program refuses)
go vet ./...
```

## Third-party software

The kc-env binary statically links these Go modules (versions pinned in `go.sum`):

| Module | License |
|---|---|
| [filippo.io/age](https://github.com/FiloSottile/age), filippo.io/hpke | BSD 3-Clause |
| golang.org/x/crypto, x/sys, x/term, x/text | BSD 3-Clause |
| [github.com/nicksnyder/go-i18n/v2](https://github.com/nicksnyder/go-i18n) | MIT |
| [github.com/BurntSushi/toml](https://github.com/BurntSushi/toml) | MIT |

kc-env also needs [age-plugin-se](https://github.com/remko/age-plugin-se) (MIT, by Remko Tronçon), built separately as described in [docs/build.md](docs/build.md). It is not included in this repository.

If you redistribute built binaries, include the licenses of these components.

## License

[0BSD](LICENSE)
