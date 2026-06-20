/**
 * QueryInfo — クエリ解析結果（semantic_query、フィルタタグ）の可視化コンポーネント。
 */

import { createFilterChip } from "./match-display.js";

let _el = null;

/**
 * QueryInfoを初期化する。
 */
export function initQueryInfo() {
  _el = document.getElementById("query-info");
}

/**
 * クエリ解析結果を描画する。
 * @param {Object} parsedQuery - ParsedQuery
 */
export function renderQueryInfo(parsedQuery) {
  if (!_el) return;

  const { semantic_query, filters = {} } = parsedQuery;
  const hasMotif = Array.isArray(filters.motif_tags) && filters.motif_tags.length > 0;
  const hasColor = Array.isArray(filters.color_tags) && filters.color_tags.length > 0;

  // フィルタが空なら非表示
  if (!hasMotif && !hasColor) {
    _el.hidden = true;
    return;
  }

  _el.hidden = false;
  _el.innerHTML = "";

  const inner = document.createElement("div");
  inner.className = "query-info-inner";

  // semantic_query テキスト
  const queryText = document.createElement("span");
  queryText.className = "query-info-text";
  queryText.textContent = `「${semantic_query}」`;
  inner.appendChild(queryText);

  // 抽出された検索条件 (カテゴリラベル付きチップ)
  const arrow = document.createElement("span");
  arrow.className = "query-info-arrow";
  arrow.textContent = "から抽出した条件:";
  inner.appendChild(arrow);

  if (hasMotif) {
    for (const tag of filters.motif_tags) {
      inner.appendChild(createFilterChip("motif", tag));
    }
  }

  if (hasColor) {
    for (const tag of filters.color_tags) {
      inner.appendChild(createFilterChip("color", tag));
    }
  }

  _el.appendChild(inner);
}

/**
 * QueryInfoを非表示にする。
 */
export function hideQueryInfo() {
  if (_el) {
    _el.hidden = true;
  }
}
