# -*- coding: utf-8 -*-
"""一次性补图脚本：用 Cloudflare Workers AI（FLUX.1 Schnell）生成横版封面。

读取 data/*.json 的路线，城市从 city_id 派生；统一“旅行画册插画”风格（gouache 质感、暖色、无文字）。
产物：frontend/public/images/{routes,cities}/<稳定ID>.webp（1024x576，q80），
同时回填 data/*.json 的 image_url、生成 frontend/public/data/cities.json，进度落 data/image-gen.json。

幂等续跑：已有成图的 ID 直接跳过；遇到 429/额度/网络错误立即退出并保留进度，重跑从缺图处继续。

环境变量（必需）：
    CF_ACCOUNT_ID   Cloudflare 账户 ID
    CF_API_TOKEN    Workers AI 权限的 API Token
用法：
    python tools/gen_images.py routes kyoto-gion-evening-walk ...   # 只跑指定路线
    python tools/gen_images.py routes --city JP:kyoto               # 指定城市的全部路线
    python tools/gen_images.py cities                               # 全部城市封面
"""
import argparse
import datetime
import json
import os
import sys
import time
import urllib.request

from PIL import Image

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
IMG_ROUTES = os.path.join(ROOT, "frontend", "public", "images", "routes")
IMG_CITIES = os.path.join(ROOT, "frontend", "public", "images", "cities")
CITIES_JSON = os.path.join(ROOT, "frontend", "public", "data", "cities.json")
STATE_PATH = os.path.join(ROOT, "data", "image-gen.json")
MODEL = "@cf/black-forest-labs/flux-1-schnell"
STYLE = (", travel album illustration, painterly gouache style, warm vibrant palette, "
         "scenic wide composition, soft light, no text, no watermark")

# 生成提示词的场景描述（英文更稳）；未列出的条目回退用“标题 + 标签”
HINTS = {
    # 京都
    "kyoto-gion-evening-walk": "Gion district Kyoto at dusk, traditional wooden machiya houses, stone-paved lane, lanterns glowing",
    "kyoto-fushimi-inari-shrine": "Fushimi Inari shrine, path through hundreds of vermilion torii gates on a mountainside, Kyoto",
    "kyoto-arashiyama-bamboo-walk": "Arashiyama bamboo grove, tall green bamboo stalks with sun rays, Kyoto Japan",
    "kyoto-kiyomizu-temple-walk": "Kiyomizu-dera wooden temple stage overlooking Kyoto city and pagoda",
    "kyoto-nishiki-market-food": "Nishiki market Kyoto, narrow covered food arcade with stalls and lanterns",
    "kyoto-philosophers-path-walk": "Philosopher's Path Kyoto, stone walkway along a canal with cherry trees",
    "kyoto-kinkakuji-pavilion": "Kinkaku-ji, the Golden Pavilion reflected in a pond, Kyoto",
    "kyoto-kurama-onsen-hike": "Kurama mountain temple in forest north of Kyoto, red pagoda among cedar trees",
    # 吉隆坡
    "kl-petronas-skybridge": "Petronas Twin Towers and KLCC park, Kuala Lumpur",
    "kl-chinatown-food-crawl": "Petaling Street Chinatown Kuala Lumpur at night, neon signs, food stalls",
    "kl-batu-caves-temple": "Batu Caves, giant golden Murugan statue at rainbow staircase, Kuala Lumpur",
    "kl-rooftop-night-view": "Kuala Lumpur skyline at night with Petronas towers, rooftop view",
    "kl-family-aquarium-day": "KLCC Aquaria underwater tunnel with rays and sharks, Kuala Lumpur",
    "kl-merdeka-heritage-walk": "Sultan Abdul Samad building at Merdeka Square, Kuala Lumpur",
    "kl-genting-highlands-day": "Genting Highlands mountain resort above the clouds, Malaysia",
    # 槟城
    "penang-street-art-walk": "George Town Penang street with murals and heritage shophouses",
    "penang-clan-jetties-sunset": "Chew Jetty wooden stilt village in George Town Penang at sunset",
    "penang-night-market-food": "Penang night food market, hawker stalls with smoke and lanterns",
    "penang-hill-funicular": "Penang Hill funicular viewpoint overlooking George Town and the sea",
    "penang-kek-lok-si-beach": "Kek Lok Si temple with pagoda on hill, Penang",
    # 兰卡威
    "langkawi-skybridge-cablecar": "Langkawi Sky Bridge curved pedestrian bridge on mountain top",
    "langkawi-island-hopping": "Langkawi tropical islands, turquoise sea and limestone islets, boat trip",
    "langkawi-mangrove-geopark": "Langkawi mangrove geopark, boat cruising limestone karsts and mangroves",
    "langkawi-cenang-sunset-jetski": "Pantai Cenang beach Langkawi at golden sunset with jet ski",
    "langkawi-dutyfree-sailing": "Sailing yacht at sunset near Langkawi islands",
    # 马六甲
    "melaka-red-square-jonker": "Dutch Square Melaka, Christ Church and red clock tower",
    "melaka-river-cruise": "Melaka river cruise along heritage buildings lit at night",
    "melaka-st-paul-hill": "St. Paul's Church ruins on St. Paul's Hill overlooking Melaka strait",
    "melaka-strait-mosque-sunset": "Melaka Straits Mosque standing in water at sunset",
    # 亚庇
    "kk-tanjung-aru-sunset": "Tanjung Aru beach Kota Kinabalu, dramatic orange sunset over calm sea",
    "kk-kinabalu-park-hiking": "Mount Kinabalu rising above clouds, Sabah Borneo",
    "kk-islands-snorkel-trip": "Snorkeling over coral and tropical fish, Tunku Abdul Rahman marine park islands",
    "kk-sunday-market-seafood": "Sunday street market Kota Kinabalu, fruit and seafood stalls",
    # 仙本那
    "semporna-kapalai-honeymoon": "Kapalai water bungalow resort on clear turquoise sea, Sabah",
    "semporna-sipadan-diving": "Scuba diver over coral wall with green turtles, Sipadan island",
    "semporna-tun-sakaran-islands": "Tun Sakaran marine park islands, blue lagoon and reef, Sabah",
    # 新山 / 怡保
    "jb-legoland-family": "Colorful Lego-style theme park entrance with brick structures, Legoland Malaysia",
    "jb-tan-hiok-nee-food": "Tan Hiok Nee heritage street Johor Bahru, old shophouses and cafes",
    "ipoh-oldtown-white-coffee": "Ipoh old town kopitiam, white coffee on marble table, heritage shophouses",
    "ipoh-limestone-temple-caves": "Ipoh limestone cave temple among karst hills, Malaysia",
    # 上海
    "shanghai-bund-architecture-walk": "The Bund Shanghai, colonial-era stone architecture along the waterfront promenade",
    "shanghai-lujiazui-skyline-night": "Lujiazui Pudong skyline at night, Oriental Pearl Tower and skyscrapers across the river",
    "shanghai-yu-garden-bazaar": "Yu Garden bazaar Shanghai, traditional curved roofs and red lanterns at City God Temple",
    "shanghai-tianzifang-art-lanes": "Tianzifang Shanghai, narrow art lanes with cafes and studios in old shikumen houses",
    "shanghai-french-concession-plane-trees": "Wukang Road French Concession Shanghai, plane trees lining heritage villas",
    "shanghai-zhujiajiao-water-town": "Zhujiajiao water town near Shanghai, stone bridges and canals with wooden boats",
    "shanghai-observatory-sunset": "Shanghai Tower observatory at sunset, city skyline bathed in golden haze",
    "shanghai-nanjing-road-shopping": "Nanjing Road pedestrian street Shanghai, neon signs and crowds at dusk",
    "shanghai-m50-art-district": "M50 art district Shanghai, contemporary galleries in a former industrial complex",
    "shanghai-xiaolongbao-food-tour": "Shanghai soup dumplings xiaolongbao in bamboo steamer baskets, food stall scene",
    "shanghai-huangpu-river-cruise": "Huangpu River night cruise, both banks of Shanghai lit up",
    "shanghai-century-park-family": "Century Park Shanghai, family picnic on lawn by the lake",
    # 厦门
    "xiamen-gulangyu-island-walk": "Gulangyu island Xiamen, colonial villas and winding lanes, sea view",
    "xiamen-hulishan-fortress": "Hulishan cannon fortress by the sea, Xiamen coastal promenade at sunset",
    "xiamen-zhongshan-road-food": "Zhongshan Road Xiamen, qilou arcade street with food stalls at night",
    "xiamen-nanputuo-temple": "Nanputuo Temple Xiamen, ornate Buddhist temple with pagoda and lotus pond",
    "xiamen-zengcuoan-village": "Zengcuo'an fishing village Xiamen, colorful guesthouses and snack street",
    "xiamen-shapowei-art-zone": "Shapowei art zone Xiamen, old harbor warehouses turned cafes and galleries",
    "xiamen-yundang-lake-walk": "Yundang Lake Xiamen, lakeside promenade with skyline reflections",
    "xiamen-wanshan-botanical-garden": "Xiamen botanical garden, tropical succulents and misty rainforest greenhouse",
    "xiamen-jimei-school-village": "Jimei school village Xiamen, distinctive Kah Kee style architecture with red roofs",
    "xiamen-beach-coffee-sunset": "Xiamen island ring road beach, coffee cart on sand at golden sunset",
}
CITY_HINTS = {
    "MY:kuala-lumpur": "Kuala Lumpur skyline with Petronas Twin Towers at dusk",
    "MY:george-town": "George Town Penang heritage streets with clan houses and street art",
    "MY:langkawi": "Langkawi archipelago aerial, turquoise sea and green islands",
    "MY:malacca": "Melaka river with Dutch Square red buildings and murals",
    "MY:kota-kinabalu": "Kota Kinabalu waterfront with Mount Kinabalu in distance",
    "MY:semporna": "Semporna sea gypsy village on stilts over turquoise water",
    "MY:johor-bahru": "Johor Bahru city skyline with strait",
    "MY:ipoh": "Ipoh old town among limestone karst mountains",
    "JP:kyoto": "Kyoto panorama with five-story pagoda and traditional rooftops",
    "JP:arashiyama": "Arashiyama bamboo grove and mountain, Kyoto",
    "CN:shanghai": "Shanghai skyline, the Bund and Lujiazui towers across Huangpu River",
    "CN:xiamen": "Xiamen Gulangyu island and coastal skyline, Egret Island",
}


def load_state():
    if os.path.exists(STATE_PATH):
        return json.load(open(STATE_PATH, encoding="utf-8"))
    return {}


def save_state(st):
    json.dump(st, open(STATE_PATH, "w", encoding="utf-8"), ensure_ascii=False, indent=1)


def load_routes():
    routes = []
    for name in sorted(os.listdir(os.path.join(ROOT, "data"))):
        if not name.endswith(".json") or name in ("sessions.json", "image-gen.json",
                                                  "image-sources.json", "cities.json"):
            continue
        routes += json.load(open(os.path.join(ROOT, "data", name), encoding="utf-8"))
    return routes


def gen_image(prompt):
    """调 Workers AI 生成一张 1024x576 图，返回 PNG bytes；错误按类型抛异常。"""
    acct = os.environ["CF_ACCOUNT_ID"]
    token = os.environ["CF_API_TOKEN"]
    url = f"https://api.cloudflare.com/client/v4/accounts/{acct}/ai/run/{MODEL}"
    # flux-1-schnell 只接受 prompt/steps，输出尺寸由模型决定，16:9 在落盘时裁剪
    body = json.dumps({"prompt": prompt, "steps": 4}).encode()
    req = urllib.request.Request(url, data=body, headers={
        "Authorization": f"Bearer {token}", "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            data = r.read()
            ctype = r.headers.get("Content-Type", "")
    except urllib.error.HTTPError as e:
        if e.code in (401, 403):
            raise SystemExit(f"认证失败（HTTP {e.code}）：检查 CF_ACCOUNT_ID / CF_API_TOKEN")
        if e.code == 429:
            raise QuotaStop("HTTP 429：额度或速率受限")
        detail = e.read()[:300]
        raise RuntimeError(f"HTTP {e.code}: {detail}")
    if "image" in ctype:
        return data
    # Workers AI 有时返回 JSON，图片以 base64 放在 result.image 里
    try:
        payload = json.loads(data)
        b64 = payload.get("result", {}).get("image", "")
        if b64:
            import base64
            return base64.b64decode(b64)
    except Exception:
        pass
    raise RuntimeError(f"响应不是图片（{ctype}）: {data[:200]}")


class QuotaStop(Exception):
    pass


def save_webp(png_bytes, out_path):
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    tmp = out_path + ".png"
    with open(tmp, "wb") as f:
        f.write(png_bytes)
    with Image.open(tmp) as im:
        im = im.convert("RGB")
        # 居中裁剪成 16:9 横版，再缩放到 1024x576
        w, h = im.size
        target_h = round(w * 9 / 16)
        if target_h <= h:
            top = (h - target_h) // 2
            im = im.crop((0, top, w, top + target_h))
        else:
            target_w = round(h * 16 / 9)
            left = (w - target_w) // 2
            im = im.crop((left, 0, left + target_w, h))
        im = im.resize((1024, 576), Image.LANCZOS)
        im.save(out_path, "WEBP", quality=80)
    os.remove(tmp)


def prompt_for_route(r):
    hint = HINTS.get(r["route_id"], f"{r['title']}, {r['city_id']}, {', '.join(r['tags'])}")
    return hint + STYLE


def cmd_routes(ids, city):
    routes = load_routes()
    st = load_state()
    done = fail = 0
    todo = routes
    if ids:
        todo = [r for r in routes if r["route_id"] in set(ids)]
    elif city:
        todo = [r for r in routes if r["city_id"] == city]
    if not todo:
        sys.exit("没有匹配的路线")
    for r in todo:
        rid = r["route_id"]
        out = os.path.join(IMG_ROUTES, rid + ".webp")
        if rid in st and os.path.exists(out):  # 续跑：已成图跳过
            print(f"skip {rid}（已有）")
            continue
        prompt = prompt_for_route(r)
        try:
            png = gen_image(prompt)
            save_webp(png, out)
        except QuotaStop as e:
            print(f"STOP {rid}: {e}；进度已保存，稍后重跑自动续跑")
            save_state(st)
            sys.exit(3)
        except Exception as e:
            print(f"FAIL {rid}: {e}")
            st[rid] = {"error": str(e)[:200], "ts": datetime.datetime.now().isoformat(timespec="seconds")}
            save_state(st)
            fail += 1
            time.sleep(2)
            continue
        st[rid] = {"prompt": prompt, "file": os.path.relpath(out, ROOT),
                   "model": MODEL, "ts": datetime.datetime.now().isoformat(timespec="seconds")}
        save_state(st)
        r["image_url"] = f"/images/routes/{rid}.webp"
        print(f"OK   {rid}")
        done += 1
        time.sleep(1)
    # 回填 data/*.json（只写本次生成成功的）
    ok_ids = {rid for rid, v in st.items() if "file" in v and rid in {r["route_id"] for r in todo}}
    for name in sorted(os.listdir(os.path.join(ROOT, "data"))):
        if not name.endswith(".json") or name in ("sessions.json", "image-gen.json",
                                                   "image-sources.json", "cities.json"):
            continue
        fp = os.path.join(ROOT, "data", name)
        data = json.load(open(fp, encoding="utf-8"))
        dirty = False
        for r in data:
            if r["route_id"] in ok_ids and os.path.exists(os.path.join(IMG_ROUTES, r["route_id"] + ".webp")):
                url = f"/images/routes/{r['route_id']}.webp"
                if r.get("image_url") != url:
                    r["image_url"] = url
                    dirty = True
        if dirty:
            json.dump(data, open(fp, "w", encoding="utf-8"), ensure_ascii=False, indent=2)
            open(fp, "a", encoding="utf-8").write("\n")
            print(f"回填 {name}")
    print(f"routes: 生成 {done}，失败 {fail}，已有 {len(todo) - done - fail}；"
          f"提醒：改完数据要 go run ./cmd/import && go run ./cmd/embed")


def cmd_cities():
    routes = load_routes()
    st = load_state()
    city_ids = sorted({r["city_id"] for r in routes})
    meta = {}
    if os.path.exists(CITIES_JSON):
        meta = json.load(open(CITIES_JSON, encoding="utf-8"))
    done = 0
    for cid in city_ids:
        key = cid.replace(":", "-")
        out = os.path.join(IMG_CITIES, key + ".webp")
        state_key = "city:" + cid
        if state_key in st and os.path.exists(out):
            print(f"skip {cid}（已有）")
        else:
            prompt = CITY_HINTS.get(cid, cid.split(":")[1].replace("-", " ")) + STYLE
            try:
                png = gen_image(prompt)
                save_webp(png, out)
            except QuotaStop as e:
                print(f"STOP {cid}: {e}；进度已保存")
                save_state(st)
                break
            except Exception as e:
                print(f"FAIL {cid}: {e}")
                st[state_key] = {"error": str(e)[:200]}
                save_state(st)
                continue
            st[state_key] = {"prompt": prompt, "file": os.path.relpath(out, ROOT),
                             "model": MODEL, "ts": datetime.datetime.now().isoformat(timespec="seconds")}
            save_state(st)
            done += 1
            time.sleep(1)
        meta[cid] = {"city_id": cid, "name": cid.split(":")[1].replace("-", " "),
                     "image_url": f"/images/cities/{key}.webp"}
    os.makedirs(os.path.dirname(CITIES_JSON), exist_ok=True)
    json.dump(meta, open(CITIES_JSON, "w", encoding="utf-8"), ensure_ascii=False, indent=1)
    print(f"cities: 新生成 {done}，总计 {len(meta)}")


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("kind", choices=["routes", "cities"])
    ap.add_argument("ids", nargs="*", help="路线 route_id；或配合 --city 省略")
    ap.add_argument("--city", default=None, help="只处理某 city_id 的路线，如 JP:kyoto")
    a = ap.parse_args()
    for var in ("CF_ACCOUNT_ID", "CF_API_TOKEN"):
        if not os.environ.get(var):
            sys.exit(f"缺少环境变量 {var}；先设置后再运行（Workers AI 免费额度，FLUX.1 Schnell 约数千张/日）")
    if a.kind == "routes":
        cmd_routes(a.ids, a.city)
    else:
        cmd_cities()
