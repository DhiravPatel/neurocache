import { Navigate, Route, Routes } from "react-router-dom";

import Layout from "./components/Layout";
import Landing from "./pages/Landing";

import Dashboard from "./pages/Dashboard";
import Analytics from "./pages/Analytics";
import KVPage from "./pages/KV";
import SemanticPage from "./pages/Semantic";
import LLMCachePage from "./pages/LLMCache";
import MemoryPage from "./pages/Memory";
import ModulesPage from "./pages/Modules";
import VectorSetsPage from "./pages/VectorSets";
import Playground from "./pages/Playground";
import CostsPage from "./pages/Costs";
import PubSubPage from "./pages/PubSub";
import LocksPage from "./pages/Locks";
import RateLimitPage from "./pages/RateLimit";
import LeaderboardsPage from "./pages/Leaderboards";
import QueuesPage from "./pages/Queues";
import StreamsPage from "./pages/Streams";
import ConversationsPage from "./pages/Conversations";
import PromptsPage from "./pages/Prompts";
import ExperimentsPage from "./pages/Experiments";
import KnowledgeGraphPage from "./pages/KnowledgeGraph";
import ModerationPage from "./pages/Moderation";
import FeatureFlagsPage from "./pages/FeatureFlags";

import DocsLayout from "./layouts/DocsLayout";
import DocsIndex         from "./pages/docs/Index";
import DocsInstallation  from "./pages/docs/Installation";
import DocsQuickStart    from "./pages/docs/QuickStart";
import DocsCommands      from "./pages/docs/Commands";
import DocsSemantic      from "./pages/docs/SemanticCache";
import DocsLLM           from "./pages/docs/LLMCache";
import DocsMemory        from "./pages/docs/Memory";
import DocsCosts         from "./pages/docs/Costs";
import DocsPubSub        from "./pages/docs/PubSub";
import DocsLocks         from "./pages/docs/Locks";
import DocsRateLimiting  from "./pages/docs/RateLimiting";
import DocsLeaderboards  from "./pages/docs/Leaderboards";
import DocsQueues        from "./pages/docs/Queues";
import DocsStreams       from "./pages/docs/Streams";
import DocsConversations from "./pages/docs/Conversations";
import DocsPrompts       from "./pages/docs/Prompts";
import DocsExperiments   from "./pages/docs/Experiments";
import DocsKnowledgeGraph from "./pages/docs/KnowledgeGraph";
import DocsModeration    from "./pages/docs/Moderation";
import DocsFeatureFlags  from "./pages/docs/FeatureFlags";
import DocsPipelining    from "./pages/docs/Pipelining";
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
        <Route path="costs"        element={<DocsCosts />} />
        <Route path="pubsub"       element={<DocsPubSub />} />
        <Route path="locks"        element={<DocsLocks />} />
        <Route path="rate-limiting" element={<DocsRateLimiting />} />
        <Route path="leaderboards" element={<DocsLeaderboards />} />
        <Route path="queues"       element={<DocsQueues />} />
        <Route path="streams"      element={<DocsStreams />} />
        <Route path="conversations" element={<DocsConversations />} />
        <Route path="prompts"       element={<DocsPrompts />} />
        <Route path="experiments"   element={<DocsExperiments />} />
        <Route path="graph"         element={<DocsKnowledgeGraph />} />
        <Route path="moderation"    element={<DocsModeration />} />
        <Route path="feature-flags" element={<DocsFeatureFlags />} />
        <Route path="pipelining"   element={<DocsPipelining />} />
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
        <Route path="costs"     element={<CostsPage />} />
        <Route path="pubsub"    element={<PubSubPage />} />
        <Route path="locks"     element={<LocksPage />} />
        <Route path="ratelimit" element={<RateLimitPage />} />
        <Route path="leaderboards" element={<LeaderboardsPage />} />
        <Route path="queues"    element={<QueuesPage />} />
        <Route path="streams"   element={<StreamsPage />} />
        <Route path="conversations" element={<ConversationsPage />} />
        <Route path="prompts"   element={<PromptsPage />} />
        <Route path="experiments" element={<ExperimentsPage />} />
        <Route path="graph"     element={<KnowledgeGraphPage />} />
        <Route path="moderation" element={<ModerationPage />} />
        <Route path="flags"     element={<FeatureFlagsPage />} />
        <Route path="modules"   element={<ModulesPage />} />
        <Route path="vectors"   element={<VectorSetsPage />} />
        <Route path="playground" element={<Playground />} />
      </Route>

      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
