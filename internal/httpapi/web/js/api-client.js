/**
 * ApiClient — image-search-api (Go) の POST /search へのfetchラッパー。
 * タイムアウト制御、エラー分類、前回リクエストキャンセルを提供する。
 *
 * このプロジェクトのバックエンド契約に合わせたアダプタ:
 *   リクエスト : { query, top_k, signed_url:true }
 *   レスポンス : { results:[{ image_uri, score, signed_url?, content_type? }] }
 * これを SPA が期待する { items:[{ thumbnail_url, title, artwork_id, score }] }
 * 形へ変換して返す。バックエンドはクエリ解析/リランキングを行わないため、
 * parsed_query と match_reasons は返さない（UI側は未定義で自然に非表示になる）。
 */

const SEARCH_ENDPOINT = "/search";
const TIMEOUT_MS = 30000;
const DEFAULT_LIMIT = 24;

let _currentController = null;

/**
 * gs:// やパス付き URI から表示用のファイル名を取り出す。
 * @param {string} uri
 * @returns {string}
 */
function basename(uri) {
  if (!uri) return "";
  const noQuery = uri.split("?")[0];
  const parts = noQuery.split("/").filter(Boolean);
  return parts.length > 0 ? parts[parts.length - 1] : noQuery;
}

/**
 * Go API のレスポンスを SPA の SearchResultItem 配列へ変換する。
 * @param {Object} data - { results: [...] }
 * @returns {{items: Object[]}}
 */
function adapt(data) {
  const results = Array.isArray(data && data.results) ? data.results : [];
  const items = results.map((r) => ({
    // signed_url を直接サムネイル/拡大表示に使う（http(s) のみ result-card 側で許可）
    thumbnail_url: r.signed_url || "",
    // 作品メタは無いので、ファイル名をタイトル代わり・完全 URI を ID として保持
    title: basename(r.image_uri),
    artwork_id: r.image_uri || "",
    score: r.score,
    content_type: r.content_type,
    // match_reasons / artist_name は無し（UI側で非表示になる）
  }));
  return { items };
}

/**
 * Search APIを呼び出す。
 * @param {string} query - 検索クエリ (1-500文字)
 * @param {number} [limit=24] - 取得件数 (1-100)
 * @returns {Promise<{items: Object[]}>}
 * @throws {ApiError}
 */
export async function search(query, limit = DEFAULT_LIMIT) {
  // 前回リクエストをキャンセル
  if (_currentController) {
    _currentController.abort();
  }

  const controller = new AbortController();
  _currentController = controller;

  const timeoutId = setTimeout(() => controller.abort(), TIMEOUT_MS);

  try {
    const response = await fetch(SEARCH_ENDPOINT, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ query, top_k: limit, signed_url: true }),
      signal: controller.signal,
    });

    if (!response.ok) {
      const detail = await response.text().catch(() => "");
      if (response.status >= 400 && response.status < 500) {
        throw new ApiError("検索条件を確認してください", response.status, detail);
      }
      throw new ApiError(
        "サーバーエラーが発生しました。しばらくしてから再試行してください",
        response.status,
        detail,
      );
    }

    const data = await response.json();
    return adapt(data);
  } catch (err) {
    if (err instanceof ApiError) {
      throw err;
    }
    if (err.name === "AbortError") {
      throw new ApiError("接続がタイムアウトしました", 0);
    }
    throw new ApiError("ネットワーク接続を確認してください", 0);
  } finally {
    clearTimeout(timeoutId);
    if (_currentController === controller) {
      _currentController = null;
    }
  }
}

export class ApiError extends Error {
  /**
   * @param {string} message - ユーザー向けメッセージ
   * @param {number} status - HTTPステータス (0 = ネットワーク/タイムアウト)
   * @param {string} [detail] - サーバーからの詳細 (UNTRUSTED — textContentのみ使用、innerHTMLに渡さないこと)
   */
  constructor(message, status, detail = "") {
    super(message);
    this.name = "ApiError";
    this.status = status;
    /** @type {string} UNTRUSTED: サーバーからの生レスポンス。textContentのみ使用すること。 */
    this.detail = detail;
  }
}
