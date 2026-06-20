/**
 * Lightbox — 検索結果画像のクリック拡大表示コンポーネント。
 * ESC / 背景クリック / ✕ ボタンで閉じる。
 */

import { createMatchList, createScoreMeter } from "./match-display.js";

let _overlay = null;

function _close() {
  if (_overlay) {
    _overlay.remove();
    _overlay = null;
    document.removeEventListener("keydown", _onKeydown);
  }
}

function _onKeydown(event) {
  if (event.key === "Escape") _close();
}

/**
 * SearchResultItem をライトボックスで拡大表示する。
 * @param {Object} item - SearchResultItem
 * @param {string} imageSrc - 表示する画像 URL (検証済みのもの)
 */
export function openLightbox(item, imageSrc) {
  _close();

  _overlay = document.createElement("div");
  _overlay.className = "lightbox";
  _overlay.addEventListener("click", (event) => {
    if (event.target === _overlay) _close();
  });

  const closeButton = document.createElement("button");
  closeButton.type = "button";
  closeButton.className = "lightbox-close";
  closeButton.setAttribute("aria-label", "閉じる");
  closeButton.textContent = "✕";
  closeButton.addEventListener("click", _close);
  _overlay.appendChild(closeButton);

  const figure = document.createElement("figure");
  figure.className = "lightbox-figure";

  const img = document.createElement("img");
  img.className = "lightbox-image";
  img.src = imageSrc;
  img.alt = item.title ?? "";
  figure.appendChild(img);

  const caption = document.createElement("figcaption");
  caption.className = "lightbox-caption";

  const title = document.createElement("div");
  title.className = "lightbox-title";
  title.textContent = item.title || item.artwork_id || "";
  caption.appendChild(title);

  if (item.artist_name && item.artist_name !== "Unknown") {
    const meta = document.createElement("div");
    meta.className = "lightbox-meta";
    meta.textContent = item.artist_name;
    caption.appendChild(meta);
  }

  // 一致度メーター + マッチ理由を横並びのパネルで表示
  const matchPanel = document.createElement("div");
  matchPanel.className = "lightbox-match-panel";
  matchPanel.appendChild(createScoreMeter(item.score));
  const matchList = createMatchList(item.match_reasons);
  if (matchList) {
    matchPanel.appendChild(matchList);
  }
  caption.appendChild(matchPanel);

  figure.appendChild(caption);
  _overlay.appendChild(figure);

  document.body.appendChild(_overlay);
  document.addEventListener("keydown", _onKeydown);
}
