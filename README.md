# se-env

English | [日本語](README.ja.md)

Keep API keys and tokens out of your shell config and `.env` files. `se-env` stores secrets in **profiles**, encrypts them with a key that lives in the Mac's **Secure Enclave**, and asks for **Touch ID** (or your login password) every time a secret is decrypted. It works fully offline.

```sh
se-env set work GITHUB_TOKEN              # hidden prompt; stored encrypted
se-env set personal GITHUB_TOKEN          # same key name, different value, different profile
se-env exec work -- gh repo list          # Touch ID once; the secret is passed only to `gh`
```

## Features

- The same variable name (e.g. `GITHUB_TOKEN`) can hold a different value in each profile.
- The decryption key is hardware-bound: it is generated inside the Secure Enclave and cannot be exported. Copies of the vault are useless on any other Mac.
- Every decryption asks for Touch ID, enforced by the Secure Enclave itself (password fallback). The dialog says which programs asked, for which profile and keys, and what will be done with them.
- No plaintext is written to disk: values stay in memory and reach a command only through its environment.
- Sessions (optional) keep selected profiles in memory for a limited time, so a burst of work needs Touch ID only once. Only programs started inside the session can use them.
- Deleting always needs Touch ID, even inside a session.
- Settings that are not secret (a URL, a user name) can be stored next to the secrets as public values. They are shown without Touch ID, and se-env stops and alerts you if their plaintext copy is changed behind its back.
- It works with any tool: `exec` passes secrets as environment variables and `header` prints HTTP headers as JSON. se-env has no tool-specific integration; [examples](#using-se-env-with-other-tools-examples) show how to combine it with other tools, AI coding assistants included.
- Messages are in English or Japanese, selected from `SE_ENV_LANG` / `LC_ALL` / `LC_MESSAGES` / `LANG`.
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

There is no build script. Build the two programs with the handful of commands in [docs/build.md](docs/build.md) (se-env with `go build` after verifying every Go module against `go.sum`; age-plugin-se from a pinned, verified commit with `swiftc`), then install them:

```sh
# Install to a directory of your choice (the "home"). ~/.local/share/se-env is a good default:
# it is outside the folders covered by macOS privacy prompts and outside iCloud Drive.
mkdir -p ~/.local/share/se-env ~/.local/bin
chmod 700 ~/.local/share/se-env
cp -Rp bin completions ~/.local/share/se-env/
ln -sf ~/.local/share/se-env/bin/se-env ~/.local/bin/se-env   # ~/.local/bin must be on your PATH

se-env init                   # create the Secure Enclave key (once)
```

The home is always "the directory above `bin/se-env`", resolved through symlinks, so you can move it later without changing any configuration. Only the symlink needs to be updated. The home cannot be redirected through an environment variable, and se-env stops if the home, the programs or the key file are writable by anyone but you. Keep the home's path at 77 characters or fewer: sessions use a Unix socket inside it, and macOS limits socket paths to 104 bytes.

### Shell setup (optional)

For Tab completion, add this to `~/.zshrc` **before** compinit (with oh-my-zsh: before `source $ZSH/oh-my-zsh.sh`):

```sh
fpath=("${${:-$HOME/.local/bin/se-env}:A:h:h}/completions" $fpath)
```

## Usage

Run `se-env help` for the full reference, including the paths in use.

### Storing and using secrets

```sh
se-env set work API_TOKEN                 # hidden prompt, or from a pipe: pbpaste | se-env set work API_TOKEN
se-env list                               # all profiles and key names (no Touch ID, values are never shown)
se-env ls work                            # key names of one profile
se-env exec work -- ./deploy.sh           # run a command with the profile's keys as environment variables
se-env get work API_TOKEN                 # print one value (avoid this where output is logged)
se-env header work API_TOKEN              # {"Authorization":"Bearer <value>"} for HTTP headers
```

When there is no terminal (for example in tools that run commands without a TTY), `set` asks for the value in a hidden macOS dialog.

Key names must be valid environment variable names. Names that change how programs start (`PATH`, `HOME`, `DYLD_*`, `LD_*`, `NODE_OPTIONS`, ...) are refused. Profile names may contain letters, digits, `.`, `_` and `-`.

### Public values

A profile often needs settings that are not secret: the URL a token belongs to, a user name, a region. Store them with `--public`:

```sh
se-env set --public work API_URL https://api.example.com   # not a secret, so it may be an argument
se-env ls work                            # API_TOKEN            (a secret: name only)
                                          # API_URL=https://api.example.com
se-env exec work -- sh -c 'curl -H "Authorization: Bearer $API_TOKEN" "$API_URL/status"'
```

A public value is encrypted inside the profile like every other key, and commands always get it from there. A plaintext copy (`vault/<profile>.public`) exists only so that `list` and `ls` can show the value without Touch ID.

Every decryption compares that copy with the encrypted profile. If something other than se-env changed or removed the copy, se-env runs nothing and prints no value. It reports the profile on standard error and in a dialog that stays on screen, and exits with status 3. `se-env ls --verify <profile>` (always Touch ID) shows what differs and rebuilds the copy from the encrypted profile.

Never use `--public` for a token or a password: the copy is plaintext on disk.

### Deleting

```sh
se-env rm work API_TOKEN OTHER_KEY        # remove keys
se-env rm --all work                      # remove every key, keep the empty profile (asks first)
se-env rm-profile work                    # remove an empty profile; refuses while keys remain
se-env rm-profile --force work            # remove a profile together with its keys (asks first)
```

Every one of these asks for Touch ID with the Secure Enclave key, even inside a session: a session lets programs read secrets, not delete them unattended.

### Sessions: Touch ID once for a while

```sh
se-env session --only work --ttl 30m -- <command>   # default TTL 30m (at most 24h); command defaults to your shell
se-env cache                              # this session: expiry, scope, cached profiles
se-env cache list                         # every running session on this Mac
se-env cache rm --current | --all | <ID>  # end sessions (Touch ID)
se-env lock                               # drop this session's cache; the next use asks again
```

Inside a session, the first decryption of each allowed profile asks for Touch ID, and later ones are answered from memory until the session ends or expires. The `se-env session` process keeps the decrypted profiles in its own memory, like ssh-agent: nothing is written to disk, no key is put in the environment, and it answers only processes that were started under `<command>`. It also enforces the TTL and `--only` itself, so a program inside the session cannot extend them.

**Every program started inside the session can read those secrets without asking you**, and so can anything such a program runs (see [the threat model](docs/encryption.md#8-threat-model)). Keep `--only` narrow, keep the TTL short, and `se-env lock` when you finish.

### Wiping everything

```sh
se-env destroy
```

This deletes the Secure Enclave key reference and every profile, and ends every session. The program itself stays, and `se-env init` starts over. It runs only from an interactive terminal and only outside a session. It shows what will be lost, then asks for `y/N`, then for a typed confirmation phrase, then waits 10 seconds, and finally asks for Touch ID. **This cannot be undone.**

### Language

Messages, help and Tab completion follow the first non-empty of `SE_ENV_LANG`, `LC_ALL`, `LC_MESSAGES` and `LANG` (`C`/`POSIX` mean English). Unknown languages fall back to English.

```sh
SE_ENV_LANG=ja se-env help
```

To always use Japanese, add this line to `~/.zshrc`. It is not needed if `LANG` or the like is already Japanese, such as `ja_JP.UTF-8`.

```sh
export SE_ENV_LANG=ja
```

se-env started by a tool such as Claude Code (for example as an MCP server's `headersHelper`) inherits that tool's environment, so the setting applies when you start the tool from this shell. In the Touch ID dialog, the first line ("“age-plugin-se” is trying to …") is written by macOS and follows the macOS language setting.

Translations live in [`cmd/se-env/locales/`](cmd/se-env/locales) as [go-i18n](https://github.com/nicksnyder/go-i18n) TOML files and are embedded in the binary. To add a language, copy `en.toml` to `<lang>.toml` (for example `de.toml`), translate the values, and run `go test ./...`. The tests fail if any message is missing, extra, or uses an unknown placeholder. Plural forms use go-i18n's CLDR categories (`one`, `other`, ...).

## Using se-env with other tools (examples)

se-env itself has no integration with any particular tool, AI assistants included. It offers generic building blocks — `exec`, `header` and `session` — and the snippets below show how to combine them. Copy and adapt them to your own configuration; they are not installed by se-env.

### Shell shortcuts (`~/.zshrc`)

```sh
# Run any command with a profile's secrets:   kx work terraform plan
kx() { local p=$1; shift; se-env exec "$p" -- "$@"; }

# Start a command in a session, so the profile asks for Touch ID only once:
#   ks work zsh
ks() { local p=$1; shift; se-env session --only "$p" -- "$@"; }

# Example: an AI coding assistant (here Claude Code) inside a 30-minute session
# for the "work" profile. Extra arguments go to the tool: ccs --resume
alias ccs='se-env session --only work --ttl 30m -- claude'
```

Inside a session, **every program started in it — an AI agent included — can use the cached secrets without asking you.** Keep `--only` to the profiles the task needs and the TTL short.

### Tools that read secrets from the environment

Wrap the command with `exec`. For example, a stdio MCP server configured in an AI assistant:

```json
"my-server": {
  "command": "/Users/<you>/.local/bin/se-env",
  "args": ["exec", "work", "--", "npx", "-y", "some-mcp-server"]
}
```

### Tools that take HTTP headers from a command

`header` prints one header as JSON. For example, Claude Code's `headersHelper` for an HTTP MCP server:

```json
"my-server": {
  "type": "http",
  "url": "https://example.com/mcp",
  "headersHelper": "/Users/<you>/.local/bin/se-env header work API_TOKEN"
}
```

Use `--name` and `--prefix` for other header formats, for example `se-env header work API_TOKEN --name X-Api-Key --prefix ''`.

## Security notes

- Losing the Mac, or erasing it, means losing access to the vault. This is by design: there is no recovery key. Keep the ability to reissue your API keys.
- Key names are stored in plaintext (`vault/<profile>.keys`) so they can be listed without Touch ID. Values are always encrypted, and public values are also kept in plaintext (`vault/<profile>.public`) for display.
- What `list` and `ls` show for a public value comes from that plaintext copy and is not verified until the next decryption. Commands never use the copy.
- Once a value is handed to a command, that command (and anything it starts) can read it. On macOS, other processes running as you can also read that command's environment (`ps -wwE`) while it runs, sessions included.
- When you move a secret into se-env, remove its old `export` from your shell startup files, reissue it, and restart your terminals: running programs keep the environment they started with.
- Anything running as you can replace se-env's own files or delete the vault. Touch ID stops silent decryption; it is not a sandbox.
- See [docs/encryption.md](docs/encryption.md) for the threat model and its limits.

## Files

```
<home>/
  bin/se-env               the tool (Go binary)
  bin/age-plugin-se        Secure Enclave plugin
  completions/_se-env      zsh completion
  identity.txt             reference to the Secure Enclave key (created by init)
  vault/<profile>.age      encrypted profile
  vault/<profile>.keys     key-name index (no values)
  vault/<profile>.public   copy of the public values (plaintext, display only)
  run/<id>.sock            socket of a running session (no secrets on disk)
```

Repository layout: `cmd/se-env/` (Go source, tests and `locales/`), `completions/`, `docs/`. `bin/`, keys and vault files are listed in `.gitignore`.

## Development

```sh
go test ./...     # unit tests, translation checks; no Secure Enclave needed (tests use software keys, which the program refuses)
go vet ./...
```

## Third-party software

The se-env binary statically links these Go modules (versions pinned in `go.sum`):

| Module | License |
|---|---|
| [filippo.io/age](https://github.com/FiloSottile/age), filippo.io/hpke | BSD 3-Clause |
| golang.org/x/crypto, x/sys, x/term, x/text | BSD 3-Clause |
| [github.com/nicksnyder/go-i18n/v2](https://github.com/nicksnyder/go-i18n) | MIT |
| [github.com/BurntSushi/toml](https://github.com/BurntSushi/toml) | MIT |

se-env also needs [age-plugin-se](https://github.com/remko/age-plugin-se) (MIT, by Remko Tronçon), built separately as described in [docs/build.md](docs/build.md). It is not included in this repository.

If you redistribute built binaries, include the licenses of these components.

## License

[0BSD](LICENSE)
