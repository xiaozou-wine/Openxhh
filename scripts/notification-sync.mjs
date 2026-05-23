#!/usr/bin/env node
/**
 * XHH 通知同步脚本
 * 用 Puppeteer 在 VPS 上常开 Chromium，登录 XHH 后定时从通知页面 DOM 提取评论数据，写入 SQLite。
 *
 * 首次运行需要手动扫码登录，之后登录态保存在 userDataDir 中。
 *
 * 用法：node scripts/notification-sync.mjs [--db /path/to/sql.db] [--interval 60]
 */

import puppeteer from "puppeteer";
import Database from "better-sqlite3";
import path from "path";
import fs from "fs";

// ── 参数解析 ──
const args = process.argv.slice(2);
function flag(name, fallback) {
  const i = args.indexOf(`--${name}`);
  return i >= 0 && args[i + 1] ? args[i + 1] : fallback;
}

const DB_PATH = flag("db", "/opt/Openxhh/sql.db");
const INTERVAL_SEC = parseInt(flag("interval", "60"), 10);
const NOTIFICATION_URL = "https://www.xiaoheihe.cn/message/home/comment";

// ── Cookie 配置 ──
// 从浏览器获取的 cookie，过期后需要更新
// 运行时可通过 --cookie 参数或 XHH_COOKIE 环境变量传入
const COOKIE_FLAG = flag("cookie", process.env.XHH_COOKIE || "");
const COOKIE_FILE = flag("cookie-file", "/opt/Openxhh/tools/xhh_cookie.txt");
const COOKIE_JSON = flag("cookie-json", "/opt/Openxhh/cookie.json");

// ── 数据库 ──
function initDB(db) {
  db.exec(`
    CREATE TABLE IF NOT EXISTS xhh_notifications (
      hash TEXT PRIMARY KEY,
      source TEXT DEFAULT 'xhh_notification',
      user_name TEXT DEFAULT '',
      comment_text TEXT DEFAULT '',
      time_text TEXT DEFAULT '',
      context_text TEXT DEFAULT '',
      updated_at BIGINT DEFAULT 0
    )
  `);
}

function upsertNotification(db, row) {
  const now = Math.floor(Date.now() / 1000);
  db.prepare(`
    INSERT INTO xhh_notifications (hash, source, user_name, comment_text, time_text, context_text, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT (hash) DO UPDATE SET
      user_name = excluded.user_name,
      comment_text = excluded.comment_text,
      time_text = excluded.time_text,
      context_text = excluded.context_text,
      updated_at = excluded.updated_at
  `).run(row.hash, row.source, row.user_name, row.comment_text, row.time_text, row.context_text, now);
}

// ── 文本处理 ──
function cleanText(el) {
  if (!el) return "";
  const clone = el.cloneNode(true);
  for (const emoji of clone.querySelectorAll(".hb-emoji")) {
    const name = emoji.getAttribute("data-emoji") || "表情";
    emoji.replaceWith(`[${name}]`);
  }
  for (const at of clone.querySelectorAll("a[data-user-id]")) {
    at.replaceWith(at.textContent.trim());
  }
  return clone.textContent.replace(/\s+/g, " ").trim();
}

function simpleHash(s) {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) | 0;
  return "n" + Math.abs(h).toString(36);
}

// ── DOM 提取 ──
async function extractNotifications(page) {
  return page.evaluate(() => {
    const items = document.querySelectorAll(".message-comment-item__container");
    const result = [];
    for (const item of items) {
      const username = item.querySelector(".message-comment-item__username")?.textContent?.trim() || "";
      const timeText = item.querySelector(".message-comment-item__desc")?.textContent?.trim() || "";

      // 评论文本：替换 emoji 和 @
      const textEl = item.querySelector(".message-comment-item__text");
      let commentText = "";
      if (textEl) {
        const clone = textEl.cloneNode(true);
        for (const emoji of clone.querySelectorAll(".hb-emoji")) {
          const name = emoji.getAttribute("data-emoji") || "表情";
          emoji.replaceWith(`[${name}]`);
        }
        for (const at of clone.querySelectorAll("a[data-user-id]")) {
          at.replaceWith(at.textContent.trim());
        }
        commentText = clone.textContent.replace(/\s+/g, " ").trim();
      }

      // 上下文：帖子标题或被回复评论
      const contentEl = item.querySelector(".message-comment-item__content");
      let contextText = "";
      if (contentEl) {
        const sub = contentEl.querySelector(".message__content-sub");
        if (sub) {
          const ctxUser = sub.querySelector(".message-content-item__username")?.textContent?.trim() || "";
          const ctxTextEl = sub.querySelector(".message-content-item__text");
          let ctxText = "";
          if (ctxTextEl) {
            const c = ctxTextEl.cloneNode(true);
            for (const emoji of c.querySelectorAll(".hb-emoji")) {
              const name = emoji.getAttribute("data-emoji") || "表情";
              emoji.replaceWith(`[${name}]`);
            }
            for (const at of c.querySelectorAll("a[data-user-id]")) {
              at.replaceWith(at.textContent.trim());
            }
            ctxText = c.textContent.replace(/\s+/g, " ").trim();
          }
          contextText = ctxUser ? `${ctxUser}: ${ctxText}` : ctxText;
        }
      }

      if (username && commentText) {
        result.push({ username, timeText, commentText, contextText });
      }
    }
    return result;
  });
}

// ── 主逻辑 ──
async function main() {
  // 检查数据库
  if (!fs.existsSync(DB_PATH)) {
    console.error(`数据库不存在: ${DB_PATH}`);
    process.exit(1);
  }
  const db = new Database(DB_PATH);
  initDB(db);
  console.log(`[通知同步] 数据库: ${DB_PATH}`);

  // 读取 cookie
  let cookieStr = COOKIE_FLAG;
  if (!cookieStr && fs.existsSync(COOKIE_FILE)) {
    cookieStr = fs.readFileSync(COOKIE_FILE, "utf8").trim();
  }
  if (!cookieStr && fs.existsSync(COOKIE_JSON)) {
    try {
      const cj = JSON.parse(fs.readFileSync(COOKIE_JSON, "utf8"));
      if (cj.cookie) cookieStr = cj.cookie;
    } catch (_) {}
  }
  if (!cookieStr) {
    console.error(`[通知同步] 缺少 cookie。请通过以下任一方式提供：`);
    console.error(`  1. --cookie "x_xhh_tokenid=xxx; user_heybox_id=93872966"`);
    console.error(`  2. XHH_COOKIE="x_xhh_tokenid=xxx; user_heybox_id=93872966"`);
    console.error(`  3. 写入 ${COOKIE_FILE}`);
    console.error(`  4. 确保 ${COOKIE_JSON} 存在且包含 cookie 字段`);
    process.exit(1);
  }

  // 启动浏览器（headless 模式）
  const browser = await puppeteer.launch({
    headless: "new",
    args: ["--no-sandbox", "--disable-setuid-sandbox", "--disable-dev-shm-usage", "--disable-gpu"],
  });

  const page = await browser.newPage();
  await page.setViewport({ width: 1280, height: 800 });
  await page.setUserAgent(
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
  );

  // 注入 cookie
  const cookies = cookieStr.split(";").filter(c => c.trim()).map((c) => {
    const [name, ...rest] = c.trim().split("=");
    return { name: name.trim(), value: rest.join("="), domain: ".xiaoheihe.cn", path: "/" };
  });
  // 从 cookie.json 补充 heybox_id
  if (fs.existsSync(COOKIE_JSON)) {
    try {
      const cj = JSON.parse(fs.readFileSync(COOKIE_JSON, "utf8"));
      if (cj.heyboxId && !cookies.find(c => c.name === "heybox_id")) {
        cookies.push({ name: "heybox_id", value: cj.heyboxId, domain: ".xiaoheihe.cn", path: "/" });
      }
    } catch (_) {}
  }
  await page.setCookie(...cookies);
  console.log(`[通知同步] 已注入 ${cookies.length} 个 cookie`);

  console.log(`[通知同步] 打开 ${NOTIFICATION_URL} ...`);
  await page.goto(NOTIFICATION_URL, { waitUntil: "networkidle2", timeout: 30000 });

  // 检查是否需要登录
  const needLogin = await page.evaluate(() => {
    return document.body.innerText.includes("登录") && !document.querySelector(".message-comment-item__container");
  });
  if (needLogin) {
    console.error("[通知同步] Cookie 已过期，请更新 cookie");
    await browser.close();
    process.exit(1);
  }

  // 首次抓取
  await new Promise((r) => setTimeout(r, 3000));
  let savedCount = await scrapeOnce(page, db);
  console.log(`[通知同步] 首次抓取完成，保存 ${savedCount} 条`);

  // 定时抓取
  setInterval(async () => {
    try {
      await page.goto(NOTIFICATION_URL, { waitUntil: "networkidle2", timeout: 30000 });
      await new Promise((r) => setTimeout(r, 2000));
      const count = await scrapeOnce(page, db);
      if (count > 0) {
        console.log(`[通知同步] 新增 ${count} 条通知`);
      }
    } catch (err) {
      console.error("[通知同步] 抓取失败:", err.message);
    }
  }, INTERVAL_SEC * 1000);

  // 保活
  setInterval(async () => {
    try { await page.evaluate(() => {}); } catch (_) {}
  }, 300_000);

  console.log(`[通知同步] 已启动，每 ${INTERVAL_SEC} 秒检查一次`);
}

async function scrapeOnce(page, db) {
  const notifications = await extractNotifications(page);
  let saved = 0;
  for (const n of notifications) {
    const hash = simpleHash(`${n.username}|${n.commentText}|${n.timeText}`);
    upsertNotification(db, {
      hash,
      source: "xhh_notification",
      user_name: n.username,
      comment_text: n.commentText,
      time_text: n.timeText,
      context_text: n.contextText,
    });
    saved++;
  }
  return saved;
}

main().catch((err) => {
  console.error("[通知同步] 致命错误:", err);
  process.exit(1);
});
