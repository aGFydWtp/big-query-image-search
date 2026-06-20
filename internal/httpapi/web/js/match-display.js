/**
 * MatchDisplay — マッチ理由・スコアの可視化コンポーネント。
 *
 * カテゴリごとの色分けはしない (例: "green" タグが青背景になる混乱を避ける)。
 * 意味は「アイコン + 構造化された日本語」で伝え、色一致だけは実際の色スウォッチを表示する。
 */

/** 色タグ名 → スウォッチ表示色 (query_parser の _COLOR_MAP 語彙と対応) */
const SWATCH_COLORS = {
  red: "#c0392b",
  blue: "#2563eb",
  green: "#2f8f4e",
  yellow: "#eab308",
  gold: "#c9a227",
  silver: "#b6bcc6",
  white: "#f8f8f4",
  black: "#23272e",
  purple: "#7c3aed",
  pink: "#ec4899",
  orange: "#ea580c",
  brown: "#8b5a2b",
  gray: "#6b7280",
};

/** 淡色スウォッチは輪郭がないと背景に溶けるため強調する */
const LIGHT_SWATCHES = new Set(["white", "silver", "yellow"]);

/** カテゴリ別アイコン (静的SVG文字列のみ。ユーザー入力は含めない) */
const ICONS = {
  mood: '<svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true"><path d="M8 1.2 9.7 6 14.5 7.7 9.7 9.4 8 14.2 6.3 9.4 1.5 7.7 6.3 6z"/></svg>',
  motif: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" aria-hidden="true"><path d="M2.2 2.2h5.1l6.5 6.5-5.1 5.1-6.5-6.5z"/><circle cx="5.2" cy="5.2" r="1.1" fill="currentColor" stroke="none"/></svg>',
  brightness: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true"><circle cx="8" cy="8" r="3"/><path d="M8 1.2v1.8M8 13v1.8M1.2 8H3M13 8h1.8M3.2 3.2l1.3 1.3M11.5 11.5l1.3 1.3M12.8 3.2l-1.3 1.3M4.5 11.5l-1.3 1.3"/></svg>',
  keyword: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><circle cx="7" cy="7" r="4.2"/><path d="M10.2 10.2 14 14"/></svg>',
};

/**
 * バックエンド reranker が生成する match_reason 文言を構造化する。
 * @param {string} reason
 * @returns {{kind: string, value?: string}}
 */
export function parseReason(reason) {
  if (reason.endsWith("モチーフ一致")) {
    return { kind: "motif", value: reason.slice(0, -"モチーフ一致".length) };
  }
  if (reason.endsWith("色一致")) {
    return { kind: "color", value: reason.slice(0, -"色一致".length) };
  }
  if (reason.includes("明るさ")) return { kind: "brightness" };
  if (reason.includes("雰囲気")) return { kind: "mood" };
  if (reason.includes("キーワード")) return { kind: "keyword" };
  return { kind: "other", value: reason };
}

/** 色スウォッチ要素を生成する。未知の色名はニュートラル表示。 */
export function createSwatch(colorName) {
  const swatch = document.createElement("span");
  swatch.className = "match-swatch";
  swatch.style.background = SWATCH_COLORS[colorName] ?? "var(--color-text-secondary)";
  if (LIGHT_SWATCHES.has(colorName)) {
    swatch.classList.add("match-swatch-light");
  }
  return swatch;
}

function _strong(value) {
  const el = document.createElement("strong");
  el.className = "match-value";
  el.textContent = value;
  return el;
}

/**
 * マッチ理由1件を行要素として生成する。
 * @param {string} reason - match_reasons の1要素
 * @returns {HTMLLIElement}
 */
export function createMatchRow(reason) {
  const parsed = parseReason(reason);
  const row = document.createElement("li");
  row.className = "match-row";

  const icon = document.createElement("span");
  icon.className = "match-icon";
  if (parsed.kind === "color") {
    icon.classList.add("match-icon-swatch");
    icon.appendChild(createSwatch(parsed.value));
  } else {
    icon.innerHTML = ICONS[parsed.kind] ?? ICONS.keyword;
  }
  row.appendChild(icon);

  const text = document.createElement("span");
  text.className = "match-text";
  switch (parsed.kind) {
    case "mood":
      text.textContent = "雰囲気が近い";
      break;
    case "brightness":
      text.textContent = "明るさが近い";
      break;
    case "keyword":
      text.textContent = "キーワードが一致";
      break;
    case "motif":
      text.append("モチーフ ");
      text.appendChild(_strong(parsed.value));
      text.append(" を含む");
      break;
    case "color":
      text.append("色 ");
      text.appendChild(_strong(parsed.value));
      text.append(" が一致");
      break;
    default:
      text.textContent = parsed.value;
  }
  row.appendChild(text);

  return row;
}

/**
 * マッチ理由のリスト要素を生成する。理由が空なら null。
 * @param {string[]|undefined} reasons
 * @returns {HTMLUListElement|null}
 */
export function createMatchList(reasons) {
  if (!reasons || reasons.length === 0) return null;
  const list = document.createElement("ul");
  list.className = "match-list";
  for (const reason of reasons) {
    list.appendChild(createMatchRow(reason));
  }
  return list;
}

const RING_RADIUS = 15.5;
const RING_CIRCUMFERENCE = 2 * Math.PI * RING_RADIUS;

/**
 * スコアを「一致度 N%」のリングメーターとして生成する。
 * @param {number|undefined} score - 0.0-1.0 のスコア
 * @returns {HTMLElement}
 */
export function createScoreMeter(score) {
  const pct = typeof score === "number"
    ? Math.round(Math.max(0, Math.min(1, score)) * 100)
    : null;

  const meter = document.createElement("div");
  meter.className = "score-meter";
  meter.setAttribute("role", "img");
  meter.setAttribute("aria-label", pct === null ? "一致度 不明" : `一致度 ${pct}%`);

  const offset = pct === null
    ? RING_CIRCUMFERENCE
    : RING_CIRCUMFERENCE * (1 - pct / 100);

  // 数字はリングの中に入れない (小さいリングと重なって読めなくなるため横に並べる)
  meter.innerHTML = `
    <svg class="score-ring" viewBox="0 0 44 44" aria-hidden="true">
      <circle class="score-ring-track" cx="22" cy="22" r="${RING_RADIUS}"/>
      <circle class="score-ring-fill" cx="22" cy="22" r="${RING_RADIUS}"
              stroke-dasharray="${RING_CIRCUMFERENCE}" stroke-dashoffset="${offset}"/>
    </svg>
    <span class="score-meter-text">
      <span class="score-meter-value">${pct === null ? "—" : pct}<small>%</small></span>
      <span class="score-meter-label">一致度</span>
    </span>
  `;
  return meter;
}

/**
 * クエリ解釈の抽出条件チップ (「モチーフ | tree」「色 | ● green」) を生成する。
 * @param {"motif"|"color"} kind
 * @param {string} value
 * @returns {HTMLElement}
 */
export function createFilterChip(kind, value) {
  const chip = document.createElement("span");
  chip.className = "filter-chip";

  const label = document.createElement("span");
  label.className = "filter-chip-kind";
  label.textContent = kind === "color" ? "色" : "モチーフ";
  chip.appendChild(label);

  const val = document.createElement("span");
  val.className = "filter-chip-value";
  if (kind === "color") {
    val.appendChild(createSwatch(value));
  }
  val.append(value);
  chip.appendChild(val);

  return chip;
}
