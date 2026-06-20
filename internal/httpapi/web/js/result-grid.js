/**
 * ResultGrid — 検索結果のレスポンシブグリッドレイアウト管理コンポーネント。
 */

import { createResultCard } from "./result-card.js";

const els = {};

/**
 * ResultGridを初期化する。
 */
export function initResultGrid() {
  els.grid = document.getElementById("result-grid");
  els.loading = document.getElementById("loading");
  els.empty = document.getElementById("empty-message");
  els.error = document.getElementById("error-message");
  els.header = document.getElementById("results-header");
  els.count = document.getElementById("results-count");
}

function _ensureInit() {
  if (!els.grid) throw new Error("initResultGrid() must be called before use");
}

/**
 * ローディング状態を表示する。
 */
export function showLoading() {
  _ensureInit();
  els.grid.innerHTML = "";
  els.loading.hidden = false;
  els.empty.hidden = true;
  els.error.hidden = true;
  els.header.hidden = true;
}

/**
 * 検索結果を描画する。
 * @param {Object[]} items - SearchResultItem の配列
 */
export function renderResults(items) {
  _ensureInit();
  els.loading.hidden = true;
  els.error.hidden = true;
  els.grid.innerHTML = "";

  if (items.length === 0) {
    els.empty.hidden = false;
    els.header.hidden = true;
    return;
  }

  els.empty.hidden = true;
  els.header.hidden = false;
  els.count.textContent = `${items.length}件の作品が見つかりました`;

  items.forEach((item, index) => {
    els.grid.appendChild(createResultCard(item, index));
  });
}

/**
 * エラーメッセージを表示する。
 * @param {string} message - ユーザー向けエラーメッセージ
 */
export function showError(message) {
  _ensureInit();
  els.loading.hidden = true;
  els.empty.hidden = true;
  els.grid.innerHTML = "";
  els.header.hidden = true;
  els.error.hidden = false;
  els.error.textContent = message;
}
