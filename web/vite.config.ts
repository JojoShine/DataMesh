import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
const basePath=(process.env.BASE_PATH??"").replace(/\/$/,"");
if(basePath&&(basePath.length>128||!/^([/][A-Za-z0-9_-]+)+$/.test(basePath)))throw new Error("Invalid BASE_PATH");
export default defineConfig({
  base: basePath + "/",
  plugins: [react()],
  build: { assetsDir: "static", sourcemap: false },
  server: { port: 15173, strictPort: true },
  test: { environment: "node" },
});
