import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeSanitize from 'rehype-sanitize';
import {hostAPI} from '../api/bridge';
function safeURL(raw:string){try{const url=new URL(raw);return (url.protocol==='http:'||url.protocol==='https:')&&!url.username&&!url.password?url.href:''}catch{return ''}}
export function MarkdownPreview({body,openExternal=(url:string)=>{void hostAPI.openExternal(url)}}:{body:string;openExternal?:(url:string)=>void}){
 return <article className="markdown"><ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeSanitize]} skipHtml urlTransform={safeURL} components={{img:({alt})=><span className="image-placeholder">{alt||'Image'} (image unavailable)</span>,a:({href,children})=>href?<a href={href} onClick={event=>{event.preventDefault();openExternal(href)}}>{children}</a>:<span>{children}</span>}}>{body}</ReactMarkdown></article>;
}
