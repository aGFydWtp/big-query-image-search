/**
 * App — SPAエントリポイント。コンポーネント初期化とイベントチェーン接続。
 */

import { search } from "./api-client.js";
import { initSearchForm } from "./search-form.js";
import { initResultGrid, showLoading, renderResults, showError } from "./result-grid.js";
import { initQueryInfo, renderQueryInfo, hideQueryInfo } from "./query-info.js";

document.addEventListener("DOMContentLoaded", () => {
  // コンポーネント初期化
  initResultGrid();
  initQueryInfo();

  const searchForm = initSearchForm({
    onSearch: handleSearch,
  });

  async function handleSearch(query) {
    searchForm.setLoading(true);
    showLoading();
    hideQueryInfo();

    try {
      const response = await search(query);

      // 結果描画
      renderResults(response.items);

      // クエリ解析結果の表示
      if (response.parsed_query) {
        renderQueryInfo(response.parsed_query);
      }
    } catch (error) {
      showError(error.message || "予期しないエラーが発生しました");
    } finally {
      searchForm.setLoading(false);
    }
  }
});
