import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function LeaderboardsDocs() {
  return (
    <>
      <h1>Leaderboards</h1>
      <p className="lead">
        Leaderboards are sorted sets read highest-first. The skip-list backing
        gives O(log n) score updates and O(log n) rank lookups, so "set a score"
        and "what's my rank?" stay fast at any size.
      </p>

      <h2>Score and rank</h2>
      <Code lang="bash">{`# RESP — it's just a sorted set
ZADD game:scores 100 alice
ZINCRBY game:scores 50 alice          # bump a score
ZREVRANGE game:scores 0 9 WITHSCORES  # top 10
ZREVRANK game:scores alice            # 0-based rank, highest = 0

# HTTP — leaderboard-shaped, ranks are 1-based and computed for you
curl localhost:8080/api/leaderboard/game:scores -d '{"member":"alice","score":100}'
# → {"member":"alice","score":100,"rank":1}
curl localhost:8080/api/leaderboard/game:scores/incr -d '{"member":"alice","by":50}'
curl localhost:8080/api/leaderboard/game:scores/top?n=10
curl localhost:8080/api/leaderboard/game:scores/rank/alice
curl localhost:8080/api/leaderboard/game:scores/around/alice?n=3   # neighbours`}</Code>

      <h2>From the SDK</h2>
      <Code lang="ts">{`const lb = cache.leaderboard;

await lb.set("game:scores", "alice", 100);   // → { member, score, rank }
await lb.incr("game:scores", "alice", 50);
const { entries } = await lb.top("game:scores", 10);
const me = await lb.rank("game:scores", "alice");      // { found, score, rank }
const near = await lb.around("game:scores", "alice", 3); // your-rank window
await lb.remove("game:scores", "alice");`}</Code>
      <Callout type="info" title="Ranks are 1-based and descending">
        The leaderboard API returns rank <code>1</code> for the top score (the
        raw <code>ZREVRANK</code> is 0-based). <code>around</code> returns a
        member plus N neighbours on each side — the classic "you're #214" view
        without paging the whole board.
      </Callout>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/leaderboards">Leaderboards page</Link> shows a
        live top-20 board that re-ranks as you set or increment scores.
      </p>
    </>
  );
}
