import { Navigate, Route, Routes } from "react-router-dom";
import Layout from "./components/Layout";
import Dashboard from "./pages/Dashboard";
import Playground from "./pages/Playground";
import MemoryPage from "./pages/Memory";
import SemanticPage from "./pages/Semantic";
import LLMCachePage from "./pages/LLMCache";
import KVPage from "./pages/KV";
import Analytics from "./pages/Analytics";

export default function App() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<Dashboard />} />
        <Route path="analytics" element={<Analytics />} />
        <Route path="kv" element={<KVPage />} />
        <Route path="semantic" element={<SemanticPage />} />
        <Route path="llm" element={<LLMCachePage />} />
        <Route path="memory" element={<MemoryPage />} />
        <Route path="playground" element={<Playground />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
