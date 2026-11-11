# ビルドとインストール（Windows）

[English](build_win.md) | 日本語

macOS の手順は [build_mac.ja.md](build_mac.ja.md) にあります。Windows 版の仕組みは [encryption.ja.md の10章](encryption.ja.md#10-windows-での仕組み) で説明しています。

Windows 版の se-env は、次の4つのファイルでできています。

| ファイル | 言語 | ソース | 役割 |
|---|---|---|---|
| `se-env.exe` | Go | `cmd/se-env` | 本体。macOS 版と同じソースから作る |
| `se_env_win.dll` | Rust | `win/se-env-win` | se-env が使う Windows API の呼び出し（ACL、ファイルのロック、プロセスの保護、Job Object、ソケットの相手、メモリ上の暗号化）。se-env.exe が cgo なしで読み込む |
| `age-plugin-winhello.exe` | Rust | `win/age-plugin-winhello` | Windows Hello の鍵（CNG）を扱う age のプラグイン |
| `se-env-ui.exe` | Rust | `win/se-env-ui` | ダイアログ（伏せ字の入力、確認、改ざんの警告） |

Rust の3つは、共通のクレート `win/sewin`（Windows API）と `win/winhello-core`（鍵の導出と形式。Windows に依存しない）を使います。

下のコマンドを PowerShell で、リポジトリの一番上のディレクトリから1つずつ実行してください。

## 必要なもの

- Windows 10 / 11（x64）と TPM 2.0
- Windows Hello（PIN、指紋、顔のどれか）を設定したユーザー
- git、Go 1.26 以降
- Rust（stable、`x86_64-pc-windows-msvc`）と、Visual Studio Build Tools の「C++ によるデスクトップ開発」。winget では次のとおりです。

```powershell
winget install --id Microsoft.VisualStudio.BuildTools --override "--quiet --wait --norestart --add Microsoft.VisualStudio.Workload.VCTools --includeRecommended"
winget install --id Rustlang.Rustup
rustup default stable-x86_64-pc-windows-msvc
```

- テストを実行するなら Git for Windows の `sh`（`C:\Program Files\Git\usr\bin\sh.exe`）。セッションのテストがシェルスクリプトを使います。

## 1. Rust の部分をビルドする

```powershell
cd win
cargo build --release --locked
cargo test --release --locked --workspace
cd ..
```

- `--locked`：`Cargo.lock` に書かれたバージョンとハッシュのクレートだけを使う。`Cargo.lock` は書き換えない
- C ランタイムは静的にリンクします（`win/.cargo/config.toml`）。できたファイルは Windows 以外の DLL を必要としません。se-env は起動後に「Microsoft の署名がある DLL だけ読み込む」設定にするので、これは必須です
- 同じファイルで、インポートする DLL を System32 だけから読み込む指定（`/DEPENDENTLOADFLAG:0x800`）と、Control Flow Guard を有効にしています。ビルドしたあと `dumpbin /loadconfig` で `Dependent Load Flag` が `0800` であることを確かめられます
- `cargo test` は、プロセスの保護（ASLR などの緩和ポリシー）が実際に有効になることも確かめます
- `winhello-core` のテストは macOS や Linux でも実行できます（`cargo test -p winhello-core`）

## 2. se-env をビルドする

```powershell
$env:GOTOOLCHAIN = "local"; $env:GOFLAGS = "-mod=readonly"
go mod download; go mod verify
go vet ./...
$env:SE_ENV_TEST_DLL_DIR = "$PWD\win\target\release"; go test ./...
New-Item -ItemType Directory -Force bin | Out-Null
go build -trimpath -o bin\se-env.exe .\cmd\se-env
```

- `SE_ENV_TEST_DLL_DIR`：テストで使う `se_env_win.dll` の場所。テストは DLL を `%TEMP%` の新しいディレクトリにコピーしてから読み込みます。se-env は、ほかの人が書き込める DLL を拒否するためです（システム以外のドライブでは、Authenticated Users に変更を許していることがよくあります）
- テストはソフトウェアの鍵を使うので、Windows Hello は出ません。本体はこの鍵を受け付けません

## 3. インストール

```powershell
$h = "$env:LOCALAPPDATA\se-env"
New-Item -ItemType Directory -Force "$h\bin" | Out-Null
Copy-Item bin\se-env.exe, win\target\release\se_env_win.dll, win\target\release\age-plugin-winhello.exe, win\target\release\se-env-ui.exe "$h\bin\"
# <ホーム>\bin を PATH に入れる（ユーザーの環境変数）
[Environment]::SetEnvironmentVariable("Path", "$h\bin;" + [Environment]::GetEnvironmentVariable("Path", "User"), "User")
```

新しいターミナルを開いてから、次を実行します。

```powershell
se-env help       # 「ファイル」のパスを確認する
se-env init       # Windows Hello の鍵を作る（最初の1回だけ。Windows Hello の確認が2回出る）
```

- ホームは常に `se-env.exe` の1つ上のディレクトリです。`%LOCALAPPDATA%` の下なら、ACL は最初から自分、SYSTEM、Administrators だけです
- se-env は、ホーム、`bin`、4つのファイル、`identity.txt`、`vault`、`run` の所有者が自分（または SYSTEM、Administrators）で、ほかの人が書き込めないことを確かめます。そうでなければ動作を止めます。`init` は `vault` に、継承を切った ACL を付けます
- `se_env_win.dll` は必ず `se-env.exe` と同じディレクトリに置いてください。se-env はほかの場所を探しません

## 更新するとき

- Rust のクレート：`cargo update -p <クレート>` で `Cargo.lock` が変わります。変更内容（特に `age-core`、`age-plugin`、`windows`、`windows-sys`）を確かめてからビルドし直してください。
- `win/se-env-win` の関数を変えたら、`ABI_VERSION` と `cmd/se-env/win_windows.go` の `dllABI` を一緒に上げてください。食い違うと se-env は DLL を使いません。

ビルドし直して置き換わるのはプログラムだけで、鍵や保管庫には影響しません。Windows Hello の鍵は Windows のユーザーの中にあり、`identity.txt` はその名前と公開鍵を指しています。
