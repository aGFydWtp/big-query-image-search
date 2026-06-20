/**
 * SearchForm — 検索入力フィールドとフォーム制御コンポーネント。
 */

const MAX_QUERY_LENGTH = 500;

/**
 * SearchFormを初期化する。
 * @param {Object} options
 * @param {function(string): void} options.onSearch - 検索実行コールバック
 */
export function initSearchForm({ onSearch }) {
  const form = document.getElementById("search-form");
  const input = document.getElementById("search-input");
  const button = document.getElementById("search-button");
  const charCount = document.getElementById("char-count");

  let _loading = false;

  // 空文字チェック → ボタン disabled 制御
  input.addEventListener("input", () => {
    const query = input.value.trim();
    button.disabled = _loading || query.length === 0;

    // 文字数カウント表示
    if (input.value.length > MAX_QUERY_LENGTH * 0.8) {
      charCount.textContent = `${input.value.length} / ${MAX_QUERY_LENGTH}`;
      charCount.hidden = false;
    } else {
      charCount.hidden = true;
    }
  });

  // submit イベント（Enter / クリック共通）
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const query = input.value.trim();
    if (_loading || query.length === 0 || query.length > MAX_QUERY_LENGTH) return;
    onSearch(query);
  });

  /**
   * ローディング状態を設定する。
   * @param {boolean} loading
   */
  function setLoading(loading) {
    _loading = loading;
    button.disabled = loading || input.value.trim().length === 0;
    button.textContent = loading ? "検索中..." : "検索";
    input.readOnly = loading;
  }

  return { setLoading };
}
