# se-env

[English](README.md) | 日本語

APIキーやトークンを、シェルの設定ファイルや `.env` に書かずに管理するためのツールです。秘密情報を**プロファイル**ごとに保管し、PC のハードウェアに守られた鍵で暗号化します。値を復号するたびに本人確認を求めます。ネットワークは一切使いません。macOS と Windows で動きます。

| OS | 鍵の保管場所 | 本人確認 | 文書 |
|---|---|---|---|
| macOS | Secure Enclave | Touch ID（使えないときはログインパスワード） | [docs/macos.ja.md](docs/macos.ja.md) |
| Windows 10 / 11 | TPM で保護された Windows Hello のコンテナ | Windows Hello（顔・指紋・PIN） | [docs/windows.ja.md](docs/windows.ja.md) |

コマンドとプロファイルの形式は両方の OS で同じです。保管庫は OS ごとに別々です。両方に共通する仕組みは [docs/design.ja.md](docs/design.ja.md) で説明しています。

```sh
se-env set work GITHUB_TOKEN              # 値は非表示で入力し、暗号化して保存
se-env set personal GITHUB_TOKEN          # 同じキー名でも、プロファイルごとに別の値
se-env exec work -- gh repo list          # 本人確認1回。値は gh にだけ渡る
```

## 特長

- 同じ変数名（例：`GITHUB_TOKEN`）に、プロファイルごとに別の値を持たせられます。
- 復号用の鍵はハードウェアに縛られています。鍵は鍵の保管場所の中で作られ、外に取り出せません。保管庫をコピーしても、他の PC では復号できません。
- 復号のたびに本人確認を求めます。これは鍵の保管場所が強制します。ダイアログには要求したプログラム、対象のプロファイルとキー、それをどうするかを表示します。
- 平文をディスクに書きません。値はメモリ上だけで扱い、実行するコマンドには環境変数としてだけ渡します。
- セッション（任意）では指定したプロファイルを一定時間メモリに保持します。まとまった作業でも本人確認は1回で済みます。使えるのは、セッションの中で起動したプログラムだけです。
- 削除にはセッションの中でも必ず本人確認を求めます。
- 秘密ではない設定（URL やユーザー名）は、公開値として秘密と同じプロファイルに保存できます。公開値は本人確認なしで表示でき、平文の写しが se-env の外で書き換えられると、se-env は実行を止めて知らせます。
- どのツールとも組み合わせられます。`exec` は秘密情報を環境変数として渡し、`header` は HTTP ヘッダーを JSON で出力します。AI のコーディング支援ツールを含め、ほかのツールとの組み合わせ方は[使用例](#ほかのツールとの組み合わせ例)で紹介しています。
- 英語と日本語に対応しています。`SE_ENV_LANG`・`LC_ALL`・`LC_MESSAGES`・`LANG` から言語を選びます。
- オフラインで動く小さな構成です。[age](https://github.com/FiloSottile/age) をライブラリとして組み込んだ Go の本体と、OS ごとの age のプラグインで動きます。実行時に通信しません。

## インストール

OS ごとの文書に、必要なもの、ビルド、インストールの手順があります。

- macOS：[docs/macos.ja.md](docs/macos.ja.md)
- Windows：[docs/windows.ja.md](docs/windows.ja.md)

どちらも、本体の1つ上のディレクトリが「ホーム」になり、鍵の参照と保管庫はそこに置きます。環境変数でホームを変えることはできません。ホーム、プログラム、鍵のファイルに自分以外が書き込める場合、se-env は動作を止めます。

## 使い方

全コマンドと、実際に使われているパスは `se-env help` で確認できます。

### 保存して使う

```sh
se-env set work API_TOKEN                 # 非表示で入力。パイプも可（macOS は pbpaste、Windows は Get-Clipboard から）
se-env list                               # 全プロファイルとキー名（本人確認なし。値は表示しない）
se-env ls work                            # 1つのプロファイルのキー名
se-env exec work -- ./deploy.sh           # キーを環境変数にしてコマンドを実行
se-env get work API_TOKEN                 # 値を1つ表示（出力が記録される場所では使わない）
se-env header work API_TOKEN              # HTTP ヘッダー用の {"Authorization":"Bearer <値>"}
```

端末がない環境（TTY なしでコマンドを実行するツールなど）では、`set` は伏せ字のダイアログで値を尋ねます。

キー名は環境変数名として有効なものにしてください。プログラムの起動のしかたを変える名前（`PATH`、`HOME`、`DYLD_*`、`LD_*`、`NODE_OPTIONS` など。Windows では `PATHEXT`、`COMSPEC` なども）は使えません。プロファイル名には英数字と `.`、`_`、`-` が使えます。

### 公開値

プロファイルには秘密ではない設定も必要になります。トークンの送り先の URL、ユーザー名、リージョンなどです。こうした値は `--public` を付けて保存します。

```sh
se-env set --public work API_URL https://api.example.com   # 秘密ではないので引数に書ける
se-env ls work                            # API_TOKEN            （秘密：名前だけ）
                                          # API_URL=https://api.example.com
se-env exec work -- sh -c 'curl -H "Authorization: Bearer $API_TOKEN" "$API_URL/status"'
```

公開値もほかのキーと同じようにプロファイルの中に暗号化して保存し、コマンドには必ずそこから渡します。平文の写し（`vault/<profile>.public`）は、`list` と `ls` が本人確認なしで値を表示するためだけにあります。

se-env は復号のたびに、この写しを暗号化されたプロファイルと照合します。se-env 以外のものが写しを書き換えたり消したりしていた場合、se-env は何も実行せず、値も出力しません。標準エラーと、画面に残るダイアログでプロファイル名を知らせ、終了コード 3 で終わります。`se-env ls --verify <profile>`（必ず本人確認）を実行すると、食い違いの内容を表示し、写しを暗号化されたプロファイルから作り直します。

トークンやパスワードには `--public` を絶対に使わないでください。写しは平文でディスクに置かれます。

### 削除

```sh
se-env rm work API_TOKEN OTHER_KEY        # キーを削除
se-env rm --all work                      # キーをすべて削除し、空のプロファイルは残す（先に確認あり）
se-env rm-profile work                    # 空のプロファイルを削除。キーが残っていれば止まる
se-env rm-profile --force work            # キーごとプロファイルを削除（先に確認あり）
```

どれも、セッションの中であっても鍵で本人確認を求めます。セッションはプログラムに秘密情報を読ませるためのもので、無人で削除させるためのものではありません。

### セッション：しばらくの間、本人確認を1回で済ませる

```sh
se-env session --only work --ttl 30m -- <コマンド>   # 期限の既定は30分（最長24時間）。コマンドの既定はシェル
se-env cache                              # このセッションの期限・対象・キャッシュ済みのプロファイル
se-env cache list                         # この PC で動いているすべてのセッション
se-env cache rm --current | --all | <ID>  # セッションを終了する（本人確認）
se-env lock                               # このセッションのキャッシュを消す（次の利用で再び本人確認）
```

セッションの中では、許可したプロファイルは最初の復号だけ本人確認を求めます。そのあとは、セッションが終わるか期限が切れるまで、メモリから応答します。復号した内容は、ssh-agent と同じように `se-env session` のプロセスが自分のメモリに持ちます。ディスクには何も書かず、環境変数にも鍵を入れません。応答するのは、`<コマンド>` の配下で起動したプロセスだけです（判定のしかたは OS ごとの文書にあります）。期限と `--only` もこのプロセスが管理するので、セッション内のプログラムが期限を延ばしたり対象を広げたりすることはできません。

**セッションの中で起動したプログラムは、どれも確認なしにキャッシュされた秘密情報を読めます**。そのプログラムが実行するものも同じです（[脅威モデル](docs/design.ja.md#7-脅威モデル共通)を参照）。`--only` の対象は絞り、期限は短くし、終わったら `se-env lock` を実行してください。

### 鍵を入れ替える

```sh
se-env rekey
```

新しい鍵を作り、すべてのプロファイルを新しい鍵で暗号化し直してから、古い鍵を削除します。プロファイルごとに古い鍵での復号が要るので、本人確認はプロファイルの数だけ求めます（Windows では、新しい鍵を作るときにさらに2回）。途中で止まっても、古い鍵は `identity.old.txt` に残り、復号にも使われます。もう一度 `se-env rekey` を実行すると続きから再開します。

### すべてを消す

```sh
se-env destroy
```

鍵と全プロファイルを削除し、すべてのセッションを終わらせます。プログラム本体は残るので、`se-env init` からやり直せます。端末から直接、セッションの外にいるときだけ実行できます。失うものを表示してから、`y/N` の確認、確認用の文字列の入力、10秒の待機、本人確認の順に進みます。**元に戻す方法はありません。**

### 言語

メッセージ、ヘルプ、Tab 補完の表示は、`SE_ENV_LANG`・`LC_ALL`・`LC_MESSAGES`・`LANG` のうち最初に値があるものに従います（`C` と `POSIX` は英語）。対応していない言語のときは英語になります。Windows では、どれもなければユーザーのロケールに従います。

```sh
SE_ENV_LANG=ja se-env help
```

いつも日本語にする設定は OS ごとの文書にあります。

翻訳は [`cmd/se-env/locales/`](cmd/se-env/locales) に [go-i18n](https://github.com/nicksnyder/go-i18n) の TOML ファイルとして置いてあり、バイナリに埋め込まれます。言語を追加するには、`en.toml` を `<言語>.toml`（例：`de.toml`）としてコピーし、値を翻訳してから `go test ./...` を実行してください。メッセージの抜けや余分、存在しないプレースホルダーがあれば、テストが失敗して知らせます。複数形は go-i18n の CLDR の区分（`one`、`other` など）で書きます。

## ほかのツールとの組み合わせ例

以下は `exec`・`header`・`session` を組み合わせる例です。自分の設定に合わせてコピーして使ってください。

### シェルのショートカット（macOS の `~/.zshrc`）

```sh
# プロファイルの秘密情報を渡してコマンドを実行する:   kx work terraform plan
kx() { local p=$1; shift; se-env exec "$p" -- "$@"; }

# セッションでコマンドを起動し、本人確認を1回で済ませる:   ks work zsh
ks() { local p=$1; shift; se-env session --only "$p" -- "$@"; }

# 例: AI のコーディング支援ツール（ここでは Claude Code）を、work プロファイルの
# 30分のセッションで起動する。追加の引数はツールに渡る: ccs --resume
alias ccs='se-env session --only work --ttl 30m -- claude'
```

セッションの中では、**そこで起動したプログラムは AI エージェントも含めて、あなたの確認なしにキャッシュされた秘密情報を使えます**。`--only` には作業に必要なプロファイルだけを指定し、期限は短めにしてください。

### 環境変数から秘密情報を読むツール

コマンドを `exec` で包みます。たとえば、AI ツールに設定する stdio 型の MCP サーバーなら次のとおりです。

```json
"my-server": {
  "command": "/Users/<you>/.local/bin/se-env",
  "args": ["exec", "work", "--", "npx", "-y", "some-mcp-server"]
}
```

Windows では `command` に `C:\\Users\\<you>\\AppData\\Local\\se-env\\bin\\se-env.exe` のように se-env.exe のフルパスを書きます。

### コマンドの出力から HTTP ヘッダーを受け取るツール

`header` はヘッダーを1つ JSON で出力します。たとえば、Claude Code の HTTP 型 MCP サーバーの `headersHelper` なら次のとおりです。

```json
"my-server": {
  "type": "http",
  "url": "https://example.com/mcp",
  "headersHelper": "/Users/<you>/.local/bin/se-env header work API_TOKEN"
}
```

ほかの形式のヘッダーには `--name` と `--prefix` を使います。例：`se-env header work API_TOKEN --name X-Api-Key --prefix ''`

## セキュリティ上の注意

- 鍵を失うと、保管庫は復号できなくなります（PC の紛失や初期化。Windows では Hello の PIN の削除や TPM のクリアも）。復旧用の鍵はなく、これは意図した仕様です。APIキーを再発行できる状態にしておいてください。
- キー名は本人確認なしで一覧できるように平文で保存しています（`vault/<profile>.keys`）。値は常に暗号化されています。公開値は表示用に平文でも置いています（`vault/<profile>.public`）。
- `list` と `ls` が表示する公開値は、この平文の写しから読んだもので、次に復号するまで照合されていません。コマンドが写しの値を使うことはありません。
- 値をコマンドに渡したあとは、そのコマンド（とそこから起動されたもの）が値を読めます。そのコマンドが動いている間は、あなたの権限で動く別のプロセスもその環境変数を読めます。セッションの中でも同じです。
- 秘密を se-env に移したら、シェルの起動ファイルから古い設定を削除し、秘密を再発行し、ターミナルを起動し直してください。動作中のプログラムは起動したときの環境変数を持ち続けます。
- あなたの権限で動くものは、se-env 自身のファイルを差し替えたり、保管庫を消したりできます。本人確認は無断の復号を止めますが、サンドボックスではありません。
- 脅威モデルと限界は、[docs/design.ja.md](docs/design.ja.md#7-脅威モデル共通) と、OS ごとの文書（[macOS](docs/macos.ja.md#10-macos-に固有の限界)、[Windows](docs/windows.ja.md#13-windows-に固有の限界)）にあります。

## ファイル構成

```
<ホーム>/
  bin/                     プログラム（macOS: se-env、age-plugin-se
                             Windows: se-env.exe、se_env_win.dll、age-plugin-winhello.exe、se-env-ui.exe）
  completions/_se-env      zsh の補完（macOS）
  identity.txt             鍵への参照（init で作成）
  vault/<profile>.age      暗号化したプロファイル
  vault/<profile>.keys     キー名の索引（値は含まない）
  vault/<profile>.public   公開値の写し（平文。表示専用）
  run/<id>.sock            動作中のセッションのソケット（秘密はディスクに置かない）
```

リポジトリの構成：`cmd/se-env/`（Go のソース、テスト、`locales/`）、`win/`（Windows 用の Rust のソース）、`completions/`、`docs/`。`bin/`、鍵や保管庫のファイルは `.gitignore` で除外しています。

## 開発

```sh
go test ./...     # 単体テストと翻訳のチェック。鍵の保管場所は不要（テストはソフトウェアの鍵を使う。本体はこの鍵を受け付けない）
go vet ./...
cd win && cargo test -p winhello-core   # Windows 版の鍵の形式（どの OS でも実行できる）
```

Windows でのテストは [docs/windows.ja.md](docs/windows.ja.md) の3章を参照してください。

## サードパーティのソフトウェア

se-env のバイナリには、次の Go モジュールが静的にリンクされています（バージョンは `go.sum` で固定）。

| モジュール | ライセンス |
|---|---|
| [filippo.io/age](https://github.com/FiloSottile/age)、filippo.io/hpke | BSD 3-Clause |
| golang.org/x/crypto、x/sys、x/term、x/text | BSD 3-Clause |
| [github.com/nicksnyder/go-i18n/v2](https://github.com/nicksnyder/go-i18n) | MIT |
| [github.com/BurntSushi/toml](https://github.com/BurntSushi/toml) | MIT |

macOS 版には [age-plugin-se](https://github.com/remko/age-plugin-se)（MIT、Remko Tronçon）も必要です。[docs/macos.ja.md](docs/macos.ja.md) の手順で別にビルドします。このリポジトリには含まれていません。

Windows 版の Rust のプログラム（`win/`）には、[age-core・age-plugin](https://github.com/str4d/rage)、[windows-sys](https://github.com/microsoft/windows-rs)、RustCrypto のクレート（いずれも MIT または Apache-2.0）などがリンクされています。バージョンと依存関係の全体は `win/Cargo.lock` で固定しています。

ビルドしたバイナリを再配布する場合は、これらのライセンスを同梱してください。

## ライセンス

[0BSD](LICENSE)
