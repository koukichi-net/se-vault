# ビルドとインストール（macOS）

[English](build_mac.md) | 日本語

Windows の手順は [build_win.ja.md](build_win.ja.md) にあります。

se-env は2つのプログラムでできています。

| プログラム | 言語 | ソース | 役割 |
|---|---|---|---|
| `se-env` | Go | このリポジトリ（`cmd/se-env`） | 本体。[age](https://github.com/FiloSottile/age) のライブラリと、`go.mod` に書かれたほかの Go モジュールを組み込んでいる |
| `age-plugin-se` | Swift | [remko/age-plugin-se](https://github.com/remko/age-plugin-se) | Secure Enclave の鍵を扱う。se-env が age のプラグインの仕組みで起動する |

実行するコマンドは下の数個だけなので、1つずつ読んでから実行できます。リポジトリの一番上のディレクトリで実行してください。git、Go 1.26 以降、Swift 6.2 以降（Xcode または Command Line Tools）が必要です。

## 1. se-env をビルドする

```sh
export GOTOOLCHAIN=local GOFLAGS=-mod=readonly
go mod download && go mod verify
go vet ./... && go test ./...
mkdir -p bin
go build -trimpath -o bin/se-env ./cmd/se-env
```

- `GOTOOLCHAIN=local`：別のバージョンの Go を自動でダウンロードしない
- `GOFLAGS=-mod=readonly`：`go.mod` と `go.sum` を書き換えない
- `go mod download` と `go mod verify`：age を含むすべてのモジュールを `go.sum` のハッシュと照合する
- `go test`：単体テストと翻訳のチェック。Secure Enclave は要りません（テストはソフトウェアの鍵を使います。本体はこの鍵を受け付けません）。セッションを試すために、短時間だけ se-env のプロセスをいくつか起動します

## 2. age-plugin-se をビルドする

プラグインは決められたコミットを**新しい空のディレクトリ**に取得し、このリポジトリの `patches/age-plugin-se-reason.patch` を当ててからビルドします。以前のチェックアウトを使い回さないでください。`swiftc` は `Sources/` にある `.swift` ファイルをすべてコンパイルするので、残っていたファイルや書き換えられたファイルがあると、コミットハッシュが一致していてもバイナリに入ってしまいます。

```sh
SE_COMMIT=a229872979aa1b46f72fd76106e39e2a097c2f1d      # タグ v0.2.1
OUT=$PWD/bin/age-plugin-se
SRC=$(mktemp -d)
git clone -q https://github.com/remko/age-plugin-se.git "$SRC"
git -C "$SRC" -c advice.detachedHead=false checkout -q "$SE_COMMIT"

# 2行とも OK と表示されること。どちらかが出なければ、ここでやめる。
test "$(git -C "$SRC" rev-parse HEAD)" = "$SE_COMMIT"   && echo "commit OK"
test -z "$(git -C "$SRC" status --porcelain --ignored)" && echo "tree OK (nothing but the commit's files)"

# Touch ID のダイアログに要求元・対象・操作を出すパッチ。変わるのが Crypto.swift だけであること。
git -C "$SRC" apply "$PWD/patches/age-plugin-se-reason.patch"
test "$(git -C "$SRC" status --porcelain --ignored)" = " M Sources/Crypto.swift" && echo "patch OK"

( cd "$SRC" && swiftc -O -parse-as-library -module-name age_plugin_se Sources/*.swift -o "$OUT" )
rm -rf "$SRC"
```

パッチは、se-env が環境変数 `SE_ENV_AUTH_REASON` で渡す文を、Touch ID のダイアログに出すようにします（`LAContext.localizedReason`）。プラグインの親プロセスが、プラグインと同じディレクトリにある `se-env` のときだけ使います。ほかのプログラムがプラグインを直接起動しても、ダイアログの文は差し替えられません。それ以外の動作は変えません。内容は `patches/age-plugin-se-reason.patch` で確かめられます。

プラグインは `swiftc` でビルドし、Apple のフレームワーク（CryptoKit と LocalAuthentication）にだけ依存します。`'21.0' is not a valid version number for macOS` のような警告は上流のコードによるもので、任意の耐量子鍵（`--pq`）にしか関係しません。ここでは無視して構いません。

### 別の方法：プラグインをパッケージマネージャで入れる

age-plugin-se は主なパッケージマネージャでも入手できます。

```sh
brew install age-plugin-se          # Homebrew
sudo port install age-plugin-se     # MacPorts
nix profile install nixpkgs#age-plugin-se
```

se-env はプラグインを、まず `<ホーム>/bin` で探し、なければ `PATH` の絶対パスのディレクトリから探します。実際にどれが使われるかは `se-env help` の「プラグイン」で確認できます。この方法では自分でビルドする代わりにパッケージのビルド工程を信頼することになります。また、上で固定しているバージョンとは違うものが入る場合があります。パッケージのプラグインにはパッチが当たっていないので、Touch ID のダイアログには要求元・対象・操作が出ず、macOS の既定の文だけになります。プラグインやそのディレクトリにグループや他人が書き込める場合、se-env は動作を止めます。一部の Intel Mac の `/usr/local/bin` がこれに当たります。その場合は、プラグインを `<ホーム>/bin` にコピーしてください。

## 3. インストール

```sh
HOME_DIR=~/.local/share/se-env
mkdir -p "$HOME_DIR/bin" ~/.local/bin
chmod 700 "$HOME_DIR"
cp -p  bin/se-env "$HOME_DIR/bin/"
[ -x bin/age-plugin-se ] && cp -p bin/age-plugin-se "$HOME_DIR/bin/"   # PATH のものを使う場合は不要
cp -Rp completions "$HOME_DIR/"
ln -sf "$HOME_DIR/bin/se-env" ~/.local/bin/se-env    # ~/.local/bin を PATH に入れておく
se-env help        # 「ファイル」のパスを確認する
se-env init        # Secure Enclave の鍵を作る（初回のインストールのときだけ）
```

ホームは常に本体の1つ上のディレクトリなので、ほかに設定するものはありません。ホームの中のものは、所有者が自分で、グループや他人が書き込めない状態にしてください。そうでなければ se-env は動作を止めます。

Tab 補完を使う場合は、README の「[シェルの設定（任意）](../README.ja.md#シェルの設定任意)」にある1行を `~/.zshrc` に追加します。「[シェルのショートカット](../README.ja.md#シェルのショートカットzshrc)」の関数や alias も、使いたいものがあれば `~/.zshrc` に写してください。

メッセージを日本語にするには、`~/.zshrc` に `export SE_ENV_LANG=ja` を追加します。`LANG` などがすでに日本語なら不要です。詳しくは README の「[言語](../README.ja.md#言語)」を参照してください。

## 更新するとき

- Go モジュール：`go get <モジュール>@<バージョン>` と `go mod tidy` で `go.mod` と `go.sum` が更新されます。ビルドし直す前に、変更内容（特に `filippo.io/age`）を確かめてください。
- age-plugin-se：上のコマンドのコミットハッシュを書き換えます。先に上流の変更内容を読んでください。特に、identity や宛先の形式に関する変更には注意が必要です。パッチが当たらなくなったら、`patches/age-plugin-se-reason.patch` を新しいコミットに合わせて作り直します。

ビルドし直して置き換わるのはバイナリだけで、鍵や保管庫には影響しません。新しいバージョンを使い始める前に、既存のプロファイルを `se-env get` で復号できることを確かめてください。
