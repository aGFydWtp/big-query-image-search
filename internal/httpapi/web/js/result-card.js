/**
 * ResultCard — 個別検索結果アイテムの表示コンポーネント。
 * カードクリックでライトボックス (拡大表示) を開く。
 */

import { openLightbox } from "./lightbox.js";
import { createMatchList, createScoreMeter } from "./match-display.js";

const PLACEHOLDER_SRC = "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='400' height='300'%3E%3Crect fill='%231e1e26' width='400' height='300'/%3E%3Ctext x='50%25' y='50%25' dominant-baseline='middle' text-anchor='middle' fill='%23666' font-size='14'%3ENo Image%3C/text%3E%3C/svg%3E";

/** http: / https: スキームのみ許可する。 */
function isSafeUrl(url) {
  try {
    const u = new URL(url, location.href);
    return u.protocol === "http:" || u.protocol === "https:";
  } catch { return false; }
}

/**
 * 検索結果アイテムからカードDOM要素を生成する。
 * @param {Object} item - SearchResultItem
 * @param {number} [index=0] - 表示順 (出現アニメーションの遅延に使用)
 * @returns {HTMLElement}
 */
export function createResultCard(item, index = 0) {
  const card = document.createElement("div");
  card.className = "result-card";
  card.style.animationDelay = `${Math.min(index * 45, 600)}ms`;

  const imageSrc = isSafeUrl(item.thumbnail_url) ? item.thumbnail_url : PLACEHOLDER_SRC;

  // サムネイル画像 (ホバーズーム用フレームで包む)
  const frame = document.createElement("div");
  frame.className = "result-card-frame";

  const img = document.createElement("img");
  img.className = "result-card-image";
  img.src = imageSrc;
  img.alt = item.title ?? "";
  img.loading = "lazy";
  img.onerror = () => {
    img.src = PLACEHOLDER_SRC;
  };
  frame.appendChild(img);
  card.appendChild(frame);

  // カードボディ
  const body = document.createElement("div");
  body.className = "result-card-body";

  // ヘッダ行: タイトル・作者 (左) + 一致度メーター (右)
  const head = document.createElement("div");
  head.className = "result-card-head";

  const titles = document.createElement("div");
  titles.className = "result-card-titles";

  const title = document.createElement("div");
  title.className = "result-card-title";
  title.textContent = item.title || item.artwork_id || "";
  titles.appendChild(title);

  // 作者名は判明している場合のみ表示 (データソース上 "Unknown" が多いため)
  if (item.artist_name && item.artist_name !== "Unknown") {
    const artist = document.createElement("div");
    artist.className = "result-card-artist";
    artist.textContent = item.artist_name;
    titles.appendChild(artist);
  }

  head.appendChild(titles);
  head.appendChild(createScoreMeter(item.score));
  body.appendChild(head);

  // マッチ理由 (アイコン + 構造化テキストの行リスト)
  const matchList = createMatchList(item.match_reasons);
  if (matchList) {
    body.appendChild(matchList);
  }

  card.appendChild(body);

  // クリックでライトボックスを開く
  card.addEventListener("click", () => {
    openLightbox(item, imageSrc);
  });

  return card;
}
