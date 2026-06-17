# リモート state バックエンド（GCS）の設定。
#
# 部分設定（partial configuration）を採用している。
# Terraform 言語仕様上、`backend "gcs"` ブロックは `var.*` を参照できないため、
# bucket / prefix はここに直書きせず、init 時に外部から注入する:
#
#   terraform init -backend-config=backend.hcl
#
# 値の雛形は backend.hcl.example を参照（コピーして backend.hcl を作成する）。
#
# state バケット自体はこのルートモジュールの管理対象外（chicken-and-egg 回避のため、
# 手動または別のブートストラップ構成で事前作成しておく）。
# GCS backend は state を自動でロックするため、複数実行者間での共有とロックを満たす。
terraform {
  backend "gcs" {}
}
