import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { getRecent, RecentRoute } from '../storage';

// 最近浏览横条：点击回到对应详情页；它也是推荐个性化的输入来源。
export default function RecentBar() {
  const [recent, setRecent] = useState<RecentRoute[]>([]);
  const navigate = useNavigate();

  useEffect(() => {
    setRecent(getRecent());
    // 每次路由变化（location.key 变化）都重新读一次，保证跨页面同步
  }, [navigate]);

  if (recent.length === 0) return null;
  return (
    <div className="recent-bar">
      <span className="recent-label">最近浏览</span>
      {recent.map((r) => (
        <button key={r.id} className="chip chip-recent" onClick={() => navigate(`/route/${r.id}`)}>
          {r.title}
        </button>
      ))}
    </div>
  );
}
