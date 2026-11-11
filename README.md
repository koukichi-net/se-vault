# se-env

English | [日本語](README.ja.md)

Keep API keys and tokens out of your shell config and `.env` files. `se-env` stores secrets in **profiles**, encrypts them with a key protected by the PC's hardware, and asks for user presence every time a secret is decrypted. It works fully offline, on macOS and Windows.

| OS | Key store | User presence | Document |
|---|---|---|---|
| macOS | Secure Enclave | Touch ID (or your login password) | [docs/macos.md](docs/macos.md) |
| Windows 10 / 11 | TPM-backed Windows Hello container | Windows Hello (face, fingerprint or PIN) | [docs/windows.md](docs/windows.md) |

The commands and the profile format are the same on both. Each OS has its own vault. What both share is described in [docs/design.md](docs/design.md).

```sh
se-env set work GITHUB_TOKEN              # hidden prompt; stored encrypted
se-env set personal GITHUB_TOKEN          # same key name, different value, different profile
se-env exec work -- gh repo list          # user presence once; the secret is passed only to `gh`
```

## Features

- The same variable name (e.g. `GITHUB_TOKEN`) can hold a different value in each profile.
- The decryption key is hardware-bound: it is generated inside the key store and cannot be exported. Copies of the vault are useless on any other PC.
- Every decryption asks for user presence, enforced by the key store itself. The dialog says which programs asked, for which profile and keys, and what will be done with them.
- No plaintext is written to disk: values stay in memory and reach a command only through its environment.
- Sessions (optional) keep selected profiles in memory for a limited time, so a burst of work needs user presence only once. Only programs started inside the session can use them.
- Deleting always needs user presence, even inside a session.
- Settings that are not secret (a URL, a user name) can be stored next to the secrets as public values. They are shown without user presence, and se-env stops and alerts you if their plaintext copy is changed behind its back.
- It works with any tool: `exec` passes secrets as environment variables and `header` prints HTTP headers as JSON. [Examples](#using-se-env-with-other-tools-examples) show how to combine it with other tools, AI coding assistants included.
- Messages are in English or Japanese, selected from `SE_ENV_LANG` / `LC_ALL` / `LC_MESSAGES` / `LANG`.
- It is offline and small: a Go program that links the [age](https://github.com/FiloSottile/age) library, plus an age plugin for each OS. No network access at runtime.

## Installation

The requirements, build and install steps are in the document for each OS:

- macOS: [docs/macos.md](docs/macos.md)
- Windows: [docs/windows.md](docs/windows.md)

On both, the directory above the program is the "home", which holds the key reference and the vault. It cannot be changed through the environment. se-env stops if anyone else can write to the home, the programs or the key file.

## Usage

Run `se-env help` for the full reference, including the paths in use.

### Storing and using secrets

```sh
se-env set work API_TOKEN                 # hidden prompt, or from a pipe (pbpaste on macOS, Get-Clipboard on Windows)
se-env list                               # all profiles and key names (no user presence, values are never shown)
se-env ls work                            # key names of one profile
se-env exec work -- ./deploy.sh           # run a command with the profile's keys as environment variables
se-env get work API_TOKEN                 # print one value (avoid this where output is logged)
se-env header work API_TOKEN              # {"Authorization":"Bearer <value>"} for HTTP headers
```

When there is no terminal (for example in tools that run commands without a TTY), `set` asks for the value in a hidden-input dialog.

Key names must be valid environment variable names. Names that change how programs start (`PATH`, `HOME`, `DYLD_*`, `LD_*`, `NODE_OPTIONS`, ...; on Windows also `PATHEXT`, `COMSPEC`, ...) are refused. Profile names may contain letters, digits, `.`, `_` and `-`.

### Public values

A profile often needs settings that are not secret: the URL a token belongs to, a user name, a region. Store them with `--public`:

```sh
se-env set --public work API_URL https://api.example.com   # not a secret, so it may be an argument
se-env ls work                            # API_TOKEN            (a secret: name only)
                                          # API_URL=https://api.example.com
se-env exec work -- sh -c 'curl -H "Authorization: Bearer $API_TOKEN" "$API_URL/status"'
```

A public value is encrypted inside the profile like every other key, and commands always get it from there. A plaintext copy (`vault/<profile>.public`) exists only so that `list` and `ls` can show the value without user presence.

Every decryption compares that copy with the encrypted profile. If something other than se-env changed or removed the copy, se-env runs nothing and prints no value. It reports the profile on standard error and in a dialog that stays on screen, and exits with status 3. `se-env ls --verify <profile>` (always user presence) shows what differs and rebuilds the copy from the encrypted profile.

Never use `--public` for a token or a password: the copy is plaintext on disk.

### Deleting

```sh
se-env rm work API_TOKEN OTHER_KEY        # remove keys
se-env rm --all work                      # remove every key, keep the empty profile (asks first)
se-env rm-profile work                    # remove an empty profile; refuses while keys remain
se-env rm-profile --force work            # remove a profile together with its keys (asks first)
```

Every one of these asks for user presence with the key, even inside a session: a session lets programs read secrets, not delete them unattended.

### Sessions: user presence once for a while

```sh
se-env session --only work --ttl 30m -- <command>   # default TTL 30m (at most 24h); command defaults to your shell
se-env cache                              # this session: expiry, scope, cached profiles
se-env cache list                         # every running session on this PC
se-env cache rm --current | --all | <ID>  # end sessions (user presence)
se-env lock                               # drop this session's cache; the next use asks again
```

Inside a session, the first decryption of each allowed profile asks for user presence, and later ones are answered from memory until the session ends or expires. The `se-env session` process keeps the decrypted profiles in its own memory, like ssh-agent: nothing is written to disk, no key is put in the environment, and it answers only processes that were started under `<command>` (how this is decided differs by OS; see the OS documents). It also enforces the TTL and `--only` itself, so a program inside the session cannot extend them.

**Every program started inside the session can read those secrets without asking you**, and so can anything such a program runs (see [the threat model](docs/design.md#7-threat-model-both-oses)). Keep `--only` narrow, keep the TTL short, and `se-env lock` when you finish.

### Replacing the key

```sh
se-env rekey
```

This creates a new key, encrypts every profile to it again, and then deletes the old key. Each profile has to be decrypted with the old key, so user presence is asked for once per profile (on Windows, twice more to create the new key). If it stops half-way, the old key stays in `identity.old.txt` and still decrypts; run `se-env rekey` again to continue.

### Wiping everything

```sh
se-env destroy
```

This deletes the key and every profile, and ends every session. The program itself stays, and `se-env init` starts over. It runs only from an interactive terminal and only outside a session. It shows what will be lost, then asks for `y/N`, then for a typed confirmation phrase, then waits 10 seconds, and finally asks for user presence. **This cannot be undone.**

### Language

Messages, help and Tab completion follow the first non-empty of `SE_ENV_LANG`, `LC_ALL`, `LC_MESSAGES` and `LANG` (`C`/`POSIX` mean English). Unknown languages fall back to English. On Windows, without any of them, the user locale is used.

```sh
SE_ENV_LANG=ja se-env help
```

How to always use Japanese is in each OS document.

Translations live in [`cmd/se-env/locales/`](cmd/se-env/locales) as [go-i18n](https://github.com/nicksnyder/go-i18n) TOML files and are embedded in the binary. To add a language, copy `en.toml` to `<lang>.toml` (for example `de.toml`), translate the values, and run `go test ./...`. The tests fail if any message is missing, extra, or uses an unknown placeholder. Plural forms use go-i18n's CLDR categories (`one`, `other`, ...).

## Using se-env with other tools (examples)

The snippets below combine `exec`, `header` and `session`. Copy and adapt them to your own configuration.

### Shell shortcuts (macOS `~/.zshrc`)

```sh
# Run any command with a profile's secrets:   kx work terraform plan
kx() { local p=$1; shift; se-env exec "$p" -- "$@"; }

# Start a command in a session, so the profile asks for user presence only once:
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

On Windows, give the full path of se-env.exe as `command`, such as `C:\\Users\\<you>\\AppData\\Local\\se-env\\bin\\se-env.exe`.

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

- Losing the key means losing access to the vault (losing or erasing the PC; on Windows also removing the Hello PIN or clearing the TPM). This is by design: there is no recovery key. Keep the ability to reissue your API keys.
- Key names are stored in plaintext (`vault/<profile>.keys`) so they can be listed without user presence. Values are always encrypted, and public values are also kept in plaintext (`vault/<profile>.public`) for display.
- What `list` and `ls` show for a public value comes from that plaintext copy and is not verified until the next decryption. Commands never use the copy.
- Once a value is handed to a command, that command (and anything it starts) can read it. While it runs, other processes running as you can also read its environment, sessions included.
- When you move a secret into se-env, remove its old setting from your shell startup files, reissue it, and restart your terminals: running programs keep the environment they started with.
- Anything running as you can replace se-env's own files or delete the vault. User presence stops silent decryption; it is not a sandbox.
- The threat model and its limits are in [docs/design.md](docs/design.md#7-threat-model-both-oses) and in each OS document ([macOS](docs/macos.md#10-limits-specific-to-macos), [Windows](docs/windows.md#13-limits-specific-to-windows)).

## Files

```
<home>/
  bin/                     programs (macOS: se-env, age-plugin-se
                             Windows: se-env.exe, se_env_win.dll, age-plugin-winhello.exe, se-env-ui.exe)
  completions/_se-env      zsh completion (macOS)
  identity.txt             reference to the key (created by init)
  vault/<profile>.age      encrypted profile
  vault/<profile>.keys     key-name index (no values)
  vault/<profile>.public   copy of the public values (plaintext, display only)
  run/<id>.sock            socket of a running session (no secrets on disk)
```

Repository layout: `cmd/se-env/` (Go source, tests and `locales/`), `win/` (Rust source for Windows), `completions/`, `docs/`. `bin/`, keys and vault files are listed in `.gitignore`.

## Development

```sh
go test ./...     # unit tests, translation checks; no key store needed (tests use software keys, which the program refuses)
go vet ./...
cd win && cargo test -p winhello-core   # key formats of the Windows version (runs on any OS)
```

For the tests on Windows, see §3 of [docs/windows.md](docs/windows.md).

## Third-party software

The se-env binary statically links these Go modules (versions pinned in `go.sum`):

| Module | License |
|---|---|
| [filippo.io/age](https://github.com/FiloSottile/age), filippo.io/hpke | BSD 3-Clause |
| golang.org/x/crypto, x/sys, x/term, x/text | BSD 3-Clause |
| [github.com/nicksnyder/go-i18n/v2](https://github.com/nicksnyder/go-i18n) | MIT |
| [github.com/BurntSushi/toml](https://github.com/BurntSushi/toml) | MIT |

The macOS version also needs [age-plugin-se](https://github.com/remko/age-plugin-se) (MIT, by Remko Tronçon), built separately as described in [docs/macos.md](docs/macos.md). It is not included in this repository.

The Rust programs of the Windows version (`win/`) link [age-core and age-plugin](https://github.com/str4d/rage), [windows-sys](https://github.com/microsoft/windows-rs), RustCrypto crates (all MIT or Apache-2.0) and others. Versions and the full dependency tree are pinned in `win/Cargo.lock`.

If you redistribute built binaries, include the licenses of these components.

## License

[0BSD](LICENSE)
