import { Route } from '../api';

// 路线卡片：有图显示图，没图显示文字占位块（图片由运营后续补 image_url）。
// 卡片上的标签是新标签页链接（城市+标签的筛选视图），不抢占当前页面。
export default function RouteCard({ route, onClick }: { route: Route; onClick?: () => void }) {
  return (
    <div className="card" onClick={onClick}>
      {route.image_url ? (
        <img className="card-img" src={route.image_url} alt={route.title} loading="lazy" />
      ) : (
        <div className="card-img card-img-placeholder">
          <span className="placeholder-city">{(route.city_id.split(':')[1] ?? route.city_id).replace(/-/g, ' ')}</span>
          <span className="placeholder-title">{route.title}</span>
        </div>
      )}
      <div className="card-body">
        <div className="card-title">{route.title}</div>
        <div className="card-tags">
          {route.tags.map((t) => (
            <a
              key={t}
              className="tag"
              href={`#/city/${encodeURIComponent(route.city_id)}?tag=${encodeURIComponent(t)}`}
              target="_blank"
              rel="noreferrer"
              onClick={(e) => e.stopPropagation()} // 点标签只开新标签页，不触发卡片跳转
            >
              {t}
            </a>
          ))}
        </div>
      </div>
    </div>
  );
}
