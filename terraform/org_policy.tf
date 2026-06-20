# ドメイン制限共有（iam.allowedPolicyMemberDomains）のプロジェクト単位の緩和（方式b）。
#
# 背景: 組織には org レベルで Domain Restricted Sharing が効いており、既定では自組織顧客の
# メンバーしか IAM に追加できない。検索 UI を別テナント（別組織）のユーザーへ IAP 公開する
# には、そのテナントの Cloud Identity 顧客IDを許可リストへ加える必要がある（加えないと
# iap.tf の別テナント domain: バインドが constraints/iam.allowedPolicyMemberDomains 違反で
# 失敗する）。許可する顧客ID自体は git 追跡外の *.tfvars から注入する（下記 var 参照）。
#
# 影響範囲の限定（重要・セキュリティ）: 緩和は **このプロジェクト単位**でのみ行い、組織全体
# には広げない。さらに inherit_from_parent=true（マージ）により、組織が許可している既存
# ドメインはそのまま継承で残し、本プロジェクトに別テナントの顧客IDを **追加** するだけに
# 留める。これにより、誤って親の許可ドメインを取りこぼす事故を避けつつ、緩和の影響を
# このプロジェクトに閉じ込める。
#
# 前提権限: 適用には org/プロジェクトの roles/orgpolicy.policyAdmin が必要。
# 付与が無い場合、apply は権限エラーになる（その場合は組織管理者に依頼）。

resource "google_org_policy_policy" "allowed_member_domains" {
  # 顧客IDの指定が無ければ緩和不要 = ポリシーを作らない（既定の組織ポリシーのまま）。
  # これにより秘匿値はコードに残らず、git 追跡外の *.tfvars 経由でのみ注入される。
  count = length(var.allowed_member_domain_customer_ids) > 0 ? 1 : 0

  name   = "projects/${var.project_id}/policies/iam.allowedPolicyMemberDomains"
  parent = "projects/${var.project_id}"

  spec {
    # 親（組織）の許可ドメインを継承しつつ、下の allowed_values を「追加」する。
    inherit_from_parent = true

    rules {
      values {
        # 追加で許可する Cloud Identity 顧客ID（git 追跡外の tfvars から注入）。
        # 自組織の顧客IDは親から継承されるため列挙不要。
        allowed_values = var.allowed_member_domain_customer_ids
      }
    }
  }

  # Org Policy API 有効化後に作成。
  depends_on = [google_project_service.required]
}

# 組織ポリシー変更の反映には伝播遅延がある。直後に IAP メンバーを追加すると
# まだ制約が古いまま評価され allowedPolicyMemberDomains 違反になりうるため、固定時間
# 待機してから iap.tf の別テナント バインドを依存させ、初回 apply での成功を狙う
# （iam.tf の time_sleep と同じ防御パターン）。なお伝播が長引いた場合は再 apply で解消する。
resource "time_sleep" "wait_org_policy_propagation" {
  count           = length(var.allowed_member_domain_customer_ids) > 0 ? 1 : 0
  depends_on      = [google_org_policy_policy.allowed_member_domains]
  create_duration = "120s"
}
