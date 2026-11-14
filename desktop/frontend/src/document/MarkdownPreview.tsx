import { useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeSanitize from "rehype-sanitize";
import { hostAPI } from "../api/bridge";
import { imageSource } from "./imageSource";
function safeURL(raw: string) {
  try {
    const url = new URL(raw);
    return (url.protocol === "http:" || url.protocol === "https:") &&
      !url.username &&
      !url.password
      ? url.href
      : "";
  } catch {
    return "";
  }
}
function Placeholder({ alt }: { alt: string }) {
  return (
    <span className="image-placeholder">
      {alt || "Image"} (image unavailable)
    </span>
  );
}
function NoteImage({ src, alt }: { src: string; alt: string }) {
  const [failed, setFailed] = useState(false);
  return failed ? (
    <Placeholder alt={alt} />
  ) : (
    <img src={src} alt={alt} loading="lazy" onError={() => setFailed(true)} />
  );
}
export function MarkdownPreview({
  body,
  notePath,
  openExternal = (url: string) => {
    void hostAPI.openExternal(url);
  },
}: {
  body: string;
  notePath?: string;
  openExternal?: (url: string) => void;
}) {
  return (
    <article className="markdown">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[rehypeSanitize]}
        skipHtml
        urlTransform={(url, key, node) =>
          key === "src" && node.tagName === "img"
            ? (imageSource(url, notePath) ?? "")
            : safeURL(url)
        }
        components={{
          img: ({ src, alt }) =>
            typeof src === "string" && src ? (
              <NoteImage key={src} src={src} alt={alt ?? ""} />
            ) : (
              <Placeholder alt={alt ?? ""} />
            ),
          a: ({ href, children }) =>
            href ? (
              <a
                href={href}
                onClick={(event) => {
                  event.preventDefault();
                  openExternal(href);
                }}
              >
                {children}
              </a>
            ) : (
              <span>{children}</span>
            ),
        }}
      >
        {body}
      </ReactMarkdown>
    </article>
  );
}
