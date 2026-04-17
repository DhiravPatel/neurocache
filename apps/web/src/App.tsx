import { Navigate, Route, Routes } from "react-router-dom";

import Layout from "./components/Layout";
import Landing from "./pages/Landing";

import Dashboard from "./pages/Dashboard";
import Analytics from "./pages/Analytics";
import KVPage from "./pages/KV";
import SemanticPage from "./pages/Semantic";
import LLMCachePage from "./pages/LLMCache";
import MemoryPage from "./pages/Memory";
import Playground from "./pages/Playground";

import DocsLayout from "./layouts/DocsLayout";
import DocsIndex         from "./pages/docs/Index";
import DocsInstallation  from "./pages/docs/Installation";
import DocsQuickStart    from "./pages/docs/QuickStart";
import DocsCommands      from "./pages/docs/Commands";
import DocsSemantic      from "./pages/docs/SemanticCache";
import DocsLLM           from "./pages/docs/LLMCache";
import DocsMemory        from "./pages/docs/Memory";
import DocsConfiguration from "./pages/docs/Configuration";
import DocsSDKs          from "./pages/docs/SDKs";
import DocsArchitecture  from "./pages/docs/Architecture";
import DocsDeployment    from "./pages/docs/Deployment";

export default function App() {
  return (
    <Routes>
      {/* Marketing landing */}
      <Route path="/" element={<Landing />} />

      {/* Documentation */}
      <Route path="/docs" element={<DocsLayout />}>
        <Route index               element={<DocsIndex />} />
        <Route path="installation" element={<DocsInstallation />} />
        <Route path="quickstart"   element={<DocsQuickStart />} />
        <Route path="commands"     element={<DocsCommands />} />
        <Route path="semantic-cache" element={<DocsSemantic />} />
        <Route path="llm-cache"    element={<DocsLLM />} />
        <Route path="memory"       element={<DocsMemory />} />
        <Route path="configuration" element={<DocsConfiguration />} />
        <Route path="sdks"         element={<DocsSDKs />} />
        <Route path="architecture" element={<DocsArchitecture />} />
        <Route path="deployment"   element={<DocsDeployment />} />
      </Route>

      {/* Product dashboard */}
      <Route path="/dashboard" element={<Layout />}>
        <Route index element={<Dashboard />} />
        <Route path="analytics" element={<Analytics />} />
        <Route path="kv"        element={<KVPage />} />
        <Route path="semantic"  element={<SemanticPage />} />
        <Route path="llm"       element={<LLMCachePage />} />
        <Route path="memory"    element={<MemoryPage />} />
        <Route path="playground" element={<Playground />} />
      </Route>

      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
