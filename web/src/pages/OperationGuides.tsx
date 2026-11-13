import {applicationURL} from "../app/basePath";
import { DownloadOutlined } from "@ant-design/icons";
import type { MouseEvent } from "react";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { navigate, usePath } from "../app/navigation";
import exchange from "../guides/exchange.md?raw";
import exchangeURL from "../guides/exchange.md?url";
import governance from "../guides/governance.md?raw";
import governanceURL from "../guides/governance.md?url";
import security from "../guides/security.md?raw";
import securityURL from "../guides/security.md?url";
import assets from "../guides/assets.md?raw";
import assetsURL from "../guides/assets.md?url";
import "./OperationGuides.css";

const guides = {
  exchange: { name: "共享交换", source: exchange, url: exchangeURL },
  governance: { name: "数据治理", source: governance, url: governanceURL },
  assets: { name: "数据资产", source: assets, url: assetsURL },
  security: { name: "数据安全", source: security, url: securityURL },
};
function sectionID(title: string) {
  return title.trim().replace(/[^\p{L}\p{N}]+/gu, "-");
}
function followLink(event: MouseEvent<HTMLAnchorElement>) {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  const url = new URL(event.currentTarget.href);
  if (url.origin !== location.origin || url.hash) return;
  event.preventDefault();
  navigate(url.pathname + url.search);
}
export default function OperationGuides() {
  const path = usePath().split("?")[0];
  const system = path === "/guides/security" ? "security" : path === "/guides/assets" ? "assets" : path === "/guides/governance" ? "governance" : "exchange";
  const guide = guides[system];
  const sections = [...guide.source.matchAll(/^## (.+)$/gm)].map((match) => match[1]);
  return <main className="operation-guides">
    <div className="operation-guides-heading">
      <div><h1>操作指南</h1><p>选择对应系统，按当前要办理的业务查阅。</p></div>
      <a className="operation-guides-download" href={applicationURL(guide.url)} download={`${guide.name}操作指南.md`}><DownloadOutlined aria-hidden="true" />下载 Markdown</a>
    </div>
    <nav className="operation-guides-systems" aria-label="选择系统指南">
      {Object.entries(guides).map(([key, item]) => <a key={key} href={applicationURL(`/guides/${key}`)} onClick={followLink} aria-current={system === key ? "page" : undefined}>{item.name}</a>)}
    </nav>
    <div className="operation-guides-layout" key={system}>
      <nav className="operation-guides-contents" aria-label="指南章节"><h2>本文目录</h2>{sections.map((title) => <a key={title} href={applicationURL(`#${sectionID(title)}`)}>{title}</a>)}</nav>
      <article className="operation-guides-document" aria-label={`${guide.name}操作指南`}>
        <Markdown skipHtml remarkPlugins={[remarkGfm]} components={{
          h2: ({ children }) => <h2 id={sectionID(String(children))}>{children}</h2>,
          a: ({ href, children }) => <a href={href?applicationURL(href):href} onClick={followLink}>{children}</a>,
          table: ({ children }) => <div className="operation-guides-table" role="region" aria-label="指南表格" tabIndex={0}><table>{children}</table></div>,
        }}>{guide.source}</Markdown>
      </article>
    </div>
  </main>;
}
