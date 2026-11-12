// k6 压测脚本：把约 85% 的流量集中到 3 条热门路线，其余打长尾。
// 观察指标：吞吐（http_reqs）、P95（http_req_duration）、错误率（http_req_failed）、
// 自定义计数器 es_recalls（响应头 X-ES-Recall=1 表示该请求触发了 ES 召回）。
//
// 用法：
//   冷缓存：先清缓存再跑  →  docker compose exec redis redis-cli FLUSHALL && k6 run k6/load.js
//   热缓存：紧接着再跑一遍（什么都不动），对比 es_recalls 和 P95。
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const ES_RECALLS = new Counter('es_recalls');

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

// 热门路线：压测时集中打这 3 条
const HOT_IDS = ['penang-hill-funicular', 'kyoto-kiyomizu-temple-walk', 'kl-chinatown-food-crawl'];

// 全部路线 id，用于长尾流量
const ALL_IDS = [
  'kl-petronas-skybridge', 'kl-chinatown-food-crawl', 'kl-batu-caves-temple', 'kl-rooftop-night-view',
  'kl-family-aquarium-day', 'kl-merdeka-heritage-walk', 'kl-genting-highlands-day',
  'penang-street-art-walk', 'penang-clan-jetties-sunset', 'penang-night-market-food',
  'penang-hill-funicular', 'penang-kek-lok-si-beach',
  'langkawi-skybridge-cablecar', 'langkawi-island-hopping', 'langkawi-mangrove-geopark',
  'langkawi-cenang-sunset-jetski', 'langkawi-dutyfree-sailing',
  'melaka-red-square-jonker', 'melaka-river-cruise', 'melaka-st-paul-hill', 'melaka-strait-mosque-sunset',
  'kk-tanjung-aru-sunset', 'kk-kinabalu-park-hiking', 'kk-islands-snorkel-trip', 'kk-sunday-market-seafood',
  'semporna-kapalai-honeymoon', 'semporna-sipadan-diving', 'semporna-tun-sakaran-islands',
  'jb-legoland-family', 'jb-tan-hiok-nee-food',
  'ipoh-oldtown-white-coffee', 'ipoh-limestone-temple-caves',
  'kyoto-gion-evening-walk', 'kyoto-fushimi-inari-shrine', 'kyoto-arashiyama-bamboo-walk',
  'kyoto-kiyomizu-temple-walk', 'kyoto-nishiki-market-food', 'kyoto-philosophers-path-walk',
  'kyoto-kinkakuji-pavilion', 'kyoto-kurama-onsen-hike',
];

export const options = {
  vus: 30,
  duration: __ENV.DURATION || '60s',
  thresholds: {
    http_req_failed: ['rate<0.01'], // 错误率 < 1%
    http_req_duration: ['p(95)<800'], // P95 < 800ms
  },
};

export default function () {
  // 85% 打热门、15% 打长尾，模拟真实场景的流量倾斜
  const id = Math.random() < 0.85
    ? HOT_IDS[Math.floor(Math.random() * HOT_IDS.length)]
    : ALL_IDS[Math.floor(Math.random() * ALL_IDS.length)];

  const res = http.get(`${BASE_URL}/v1/items/${id}/recommendations`, {
    timeout: '5s',
    tags: { item_type: HOT_IDS.includes(id) ? 'hot' : 'long_tail' },
  });

  check(res, {
    'status is 200': (r) => r.status === 200,
  });

  if (res.headers['X-Es-Recall'] === '1') {
    ES_RECALLS.add(1);
  }
}
