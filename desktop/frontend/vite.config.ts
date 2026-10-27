import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
export default defineConfig({
  plugins: [react()],
  base: "./",
  build: {
    rollupOptions: {
      output: {
        manualChunks: {
          editor: [
            "@codemirror/state",
            "@codemirror/view",
            "@codemirror/commands",
            "@codemirror/lang-markdown",
          ],
          markdown: ["react-markdown", "remark-gfm", "rehype-sanitize"],
        },
      },
    },
  },
});
