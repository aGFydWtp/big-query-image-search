# Requirements Document

## Project Description (Input)

画像セマンティック検索システムの「検索（クエリ）」層を定義する。検索利用者（クライアントアプリケーション）は、テキストクエリで GCS 上の画像を意味的に検索したいが、現状は BigQuery 上に取込済みの埋め込み（`image_embeddings` テーブル）と VECTOR INDEX が存在するのみで、それらを利用する検索インターフェースが存在しない。本仕様により、Cloud Run 上の HTTP API が、テキストクエリを取込と同一のモデル（`gemini-embedding-2`・同一次元）で埋め込み、同一の BigQuery クエリ内で `VECTOR_SEARCH` を実行し、一致した画像の参照情報（URI / 署名付き URL / スコア）を返却できるようになる。

本仕様は上流 2 仕様の共有契約に厳密に従う消費者である。基盤リソース（GCS バケット・BigQuery dataset・Cloud Run 実行用サービスアカウント・IAM）は `gcp-infrastructure` が、`image_embeddings` テーブルスキーマ・埋め込み次元（3072）・リモートモデル名（`gemini-embedding-2`）・VECTOR INDEX 距離タイプ（`COSINE`）は `image-ingestion-pipeline` が source of truth として所有する。本仕様はこれらを再定義しない。

## Introduction

本仕様 `image-search-api` は、Cloud Run 上で稼働するステートレスなテキスト→画像検索 HTTP API を定義する。API はリクエストごとに、(1) クエリテキストを上流取込と同一のリモートモデルで埋め込み、(2) 同一 BigQuery クエリ内で `image_embeddings` テーブル / VECTOR INDEX に対し `VECTOR_SEARCH` を実行し、(3) 一致画像の URI・スコア・必要に応じ GCS 署名付き URL を整形して返却する。あわせて、Cloud Run へのデプロイ／ランタイム構成を定義する。

本仕様が所有するのは HTTP API のリクエスト/レスポンス契約、クエリ埋め込み + `VECTOR_SEARCH` の同一クエリ実行、結果整形、デプロイ構成である。埋め込みの事前生成・テーブル/インデックス作成（`image-ingestion-pipeline` 所有）、基盤リソースのプロビジョニング（`gcp-infrastructure` 所有）は本仕様の責務外である。

## Boundary Context

- **In scope**:
  - テキスト→画像検索 HTTP エンドポイント（リクエスト/レスポンス契約）
  - クエリ埋め込み生成と `VECTOR_SEARCH` の同一 BigQuery クエリ内実行
  - 検索結果の整形（画像 URI / GCS 署名付き URL / 類似度スコア）
  - 検索パラメータ（`top_k`、結果件数等）の受理とバリデーション
  - 基本的なエラー処理（不正入力・上流障害・空結果）
  - Cloud Run へのデプロイ／ランタイム構成（コンテナ、サービス設定、環境変数によるパラメータ注入、実行用サービスアカウントの割当）
- **Out of scope**:
  - 埋め込みの事前生成・`image_embeddings` テーブルの作成・VECTOR INDEX の作成（`image-ingestion-pipeline` 所有）
  - GCS バケット・BigQuery dataset・Cloud Run 実行用サービスアカウント・IAM ロールバインドの作成（`gcp-infrastructure` 所有）
  - リモートモデル名・埋め込み次元・距離タイプの定義（`image-ingestion-pipeline` が source of truth）
  - フロントエンド UI
  - 認証・認可・レート制限などのエンタープライズ機能（明示要求があるまで）
- **Adjacent expectations**:
  - 本仕様は `gcp-infrastructure` が払い出した Cloud Run 実行用サービスアカウント・IAM・BigQuery dataset・GCS バケットを前提に検索 API を実装する。
  - 本仕様は `image-ingestion-pipeline` が定義した共有契約（テーブル名 `image_embeddings`、列 `image_uri` / `embedding`(dim=3072) / `content_type` / `generated_at`、モデル名 `gemini-embedding-2`、距離タイプ `COSINE`）に厳密に従う。これらの変更は本仕様の再検証 Trigger となる。
  - 環境依存値（`project_id`, `region`, `dataset_id`, テーブル名、モデル名、バケット）は上流出力からパラメータとして注入され、ハードコードしない。

## Requirements

### Requirement 1: 検索 HTTP エンドポイントとリクエスト/レスポンス契約

**Objective:** 検索クライアントとして、テキストクエリを送信して意味的に一致する画像の一覧を取得したい。そうすることで、UI やバッチ処理から画像セマンティック検索を利用できる。

#### Acceptance Criteria

1. WHEN クライアントが検索エンドポイントへテキストクエリを含む POST リクエストを送信する THEN image-search-api SHALL クエリ文字列と任意の `top_k` パラメータを受理し、検索結果配列を含む JSON レスポンスを返す
2. WHERE レスポンスの各検索結果 THE image-search-api SHALL 画像 URI・類似度スコア・（要求時の）GCS 署名付き URL を含むフィールドを返す
3. WHEN リクエストに `top_k` が指定されない THEN image-search-api SHALL あらかじめ定義された既定件数を用いて検索結果を返す
4. WHEN リクエストが受理され検索が成功する THEN image-search-api SHALL HTTP 200 と、スコア降順（最も類似する順）に並んだ結果配列を返す
5. THE image-search-api SHALL レスポンスの JSON スキーマ（フィールド名・型）を安定契約として定義し、クライアントが解釈可能な構造で返す

### Requirement 2: クエリ埋め込みと VECTOR_SEARCH の同一クエリ実行（共有契約整合）

**Objective:** 検索の正確性を保証する立場として、クエリ埋め込みが取込と同一のベクトル空間で生成され、探索が取込時インデックスと整合した距離尺度で行われることを保証したい。そうすることで、テキストと画像が比較可能になり検索が破綻しない。

#### Acceptance Criteria

1. WHEN 検索リクエストを処理する THEN image-search-api SHALL クエリテキストの埋め込み生成を、取込（`image-ingestion-pipeline`）と同一のリモートモデル `gemini-embedding-2` を用いて BigQuery 上で実行する
2. WHEN クエリ埋め込みを生成する THEN image-search-api SHALL 取込側と同一の埋め込み次元（3072）で生成し、次元不一致が発生しないようにする
3. WHEN ベクトル探索を実行する THEN image-search-api SHALL クエリ埋め込み生成と `VECTOR_SEARCH` を同一の BigQuery クエリ内で実行し、BigQuery への往復回数を削減する
4. WHEN `VECTOR_SEARCH` を実行する THEN image-search-api SHALL 共有契約の `image_embeddings` テーブルの `embedding` 列を対象とし、取込時 VECTOR INDEX と同一の距離タイプ `COSINE` を用いる
5. THE image-search-api SHALL モデル名・埋め込み次元・距離タイプ・テーブル名・列名を `image-ingestion-pipeline` の共有契約から消費し、本仕様内で独自に再定義しない
6. IF 共有契約（モデル名・次元・距離タイプ・テーブル/列名）の値が上流で変更される THEN image-search-api SHALL 再検証を必要とし、注入パラメータの更新のみで追従できる構造とする

### Requirement 3: 検索結果の整形（URI / 署名付き URL / スコア）

**Objective:** 検索クライアントとして、返却された画像をそのまま参照・表示したい。そうすることで、追加の権限解決なしに検索結果を利用できる。

#### Acceptance Criteria

1. WHEN `VECTOR_SEARCH` が一致行を返す THEN image-search-api SHALL 各一致行から画像 URI（GCS の `gs://` URI）と類似度スコアを抽出してレスポンス項目に整形する
2. WHERE クライアントが直接アクセス可能な URL を要求する THE image-search-api SHALL 一致画像の GCS オブジェクトに対し有効期限付きの署名付き URL を生成して返す
3. WHEN 署名付き URL を生成する THEN image-search-api SHALL Cloud Run 実行用サービスアカウントの権限を用いて、対象 GCS バケットのオブジェクトに対してのみ署名 URL を発行する
4. WHEN 類似度スコアを返す THEN image-search-api SHALL `VECTOR_SEARCH` の距離出力をクライアントが解釈可能なスコア表現に整形し、その意味（距離か類似度か）を一貫させる
5. IF 一致行のメタデータ（`content_type` 等）が利用可能 THEN image-search-api SHALL それらを任意のレスポンスフィールドとして付加できる

### Requirement 4: エラー処理と境界条件

**Objective:** API 運用者として、不正入力や上流障害が予測可能なレスポンスとして返ることを保証したい。そうすることで、クライアントが堅牢にエラーを扱える。

#### Acceptance Criteria

1. IF リクエストにクエリテキストが含まれない、または空である THEN image-search-api SHALL HTTP 400 とエラー内容を示すレスポンスを返し、BigQuery クエリを実行しない
2. IF `top_k` が許容範囲外（非正・上限超過等）である THEN image-search-api SHALL HTTP 400 を返すか、定義済みの安全な範囲へ丸める方針を一貫して適用する
3. IF BigQuery クエリ実行が失敗する（埋め込み生成失敗・クエリエラー・タイムアウト等）THEN image-search-api SHALL HTTP 5xx と障害を示すエラーレスポンスを返し、内部例外詳細をクライアントへ漏洩しない
4. WHEN 検索が成功したが一致が 0 件である THEN image-search-api SHALL HTTP 200 と空の結果配列を返す
5. IF 署名付き URL 生成が失敗する THEN image-search-api SHALL 当該結果項目について URL を省略するか障害を明示し、他の結果返却を妨げない
6. THE image-search-api SHALL すべてのエラーレスポンスを、Requirement 1 のレスポンス契約と整合する判別可能な構造で返す

### Requirement 5: Cloud Run デプロイ・ランタイム構成とパラメータ注入

**Objective:** デプロイ担当者として、検索 API を上流が払い出した基盤の上に最小権限で再現可能にデプロイしたい。そうすることで、環境差異やハードコードに依存せず運用できる。

#### Acceptance Criteria

1. WHEN サービスをデプロイする THEN image-search-api SHALL コンテナ化されたアプリケーションを Cloud Run サービスとして定義し、上流が払い出した Cloud Run 実行用サービスアカウントを割り当てる
2. WHERE 環境依存値（`project_id`, `region`, `dataset_id`, `image_embeddings` テーブル名, モデル名 `gemini-embedding-2`, 対象 GCS バケット）THE image-search-api SHALL これらを環境変数等で外部から注入し、アプリケーションコードへハードコードしない
3. THE image-search-api SHALL ステートレスに動作し、リクエスト間で可変な永続状態を保持しない
4. WHEN BigQuery / Vertex AI / GCS にアクセスする THEN image-search-api SHALL 実行用サービスアカウントに付与済みの最小権限（BigQuery 実行・データ読取、Vertex AI 利用、GCS 署名/読取）のみを利用し、追加の過剰権限を要求しない
5. WHERE BigQuery dataset・GCS バケット・リモートモデルのロケーション THE image-search-api SHALL 上流の単一 `region` と整合する設定で動作する
6. WHEN デプロイ構成を運用する THEN image-search-api SHALL 再現可能なデプロイ手順（ビルド・デプロイ・必須環境変数）を提供し、ローカルでの起動・検証手順を含む
