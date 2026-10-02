#!/usr/bin/env python3
"""Генерирует HTML-фикстуры страниц Авито (SSR-JSON в data-mfe-state и __initialData__)."""
import json, html, urllib.parse, pathlib

OUT = pathlib.Path(__file__).parent / "testdata"
OUT.mkdir(exist_ok=True)

item1 = {
    "id": "3221234567",
    "urlPath": "/moskva/kvartiry/2-k_kvartira_54_m2_na_5_et._3221234567",
    "title": "2-к. квартира, 54 м², 5/9 эт.",
    "priceDetailed": {"value": 12500000, "currency": "RUB",
                      "pricePerPart": {"value": 231481, "title": "за м²", "unit": "м2"}},
    "params": [
        {"title": "Количество комнат", "value": "2"},
        {"title": "Общая площадь", "value": "54 м²"},
        {"title": "Этаж", "value": "5 из 9"},
    ],
    "address": "Москва, улица Ленина, 10",
    "geo": {"formattedAddress": "Москва, улица Ленина, 10", "region": "Москва",
            "city": "Москва", "district": "ЦАО",
            "coordinates": {"lat": 55.751244, "lng": 37.618423}},
    "images": [{"640x480": {"url": "https://90.img.avito.st/640x480/111.jpg"},
                "208x208": {"url": "https://90.img.avito.st/208x208/111s.jpg"}}],
    "time": 1696200000,
    "sellerId": 12345,
}
item2 = {
    "id": "3221234568",
    "urlPath": "/moskva/kvartiry/studiya_28_m2_na_2_et._3221234568",
    "title": "Студия, 28 м², 2/17 эт.",
    "priceDetailed": {"value": 6900000, "currency": "RUB"},
    "params": [
        {"title": "Количество комнат", "value": "Студия"},
        {"title": "Общая площадь", "value": "28,5 м²"},
        {"title": "Этаж", "value": "2 из 17"},
    ],
    "address": "Москва, проспект Мира, 5",
    "geo": {"formattedAddress": "Москва, проспект Мира, 5", "region": "Москва", "city": "Москва",
            "coordinates": {"lat": 55.788874, "lng": 37.632648}},
    "images": [{"640x480": {"url": "https://91.img.avito.st/640x480/222.jpg"}}],
    "time": 1696286400,
    "sellerId": 67890,
}

# --- страница выдачи (современный формат data-mfe-state) ---
catalog = {
    "loaderData": {
        "data": {
            "catalog": {
                "items": [item1, item2],
                "count": 2051,
            }
        }
    }
}
mfe_html = (
    "<!DOCTYPE html><html><head><title>Квартиры в Москве — Авито</title></head><body>"
    '<script type="mime/invalid" data-mfe-state="true">'
    + html.escape(json.dumps(catalog, ensure_ascii=False), quote=True)
    + "</script></body></html>"
)
(OUT / "search_mfe.html").write_text(mfe_html, encoding="utf-8")

# --- карточка объявления (data-mfe-state, loaderData.data) ---
detail = {
    "loaderData": {
        "data": {
            "itemId": "3221234567",
            "urlPath": item1["urlPath"],
            "title": item1["title"],
            "priceDetailed": item1["priceDetailed"],
            "description": "<p>Просторная квартира с ремонтом. окна во двор.</p>",
            "params": [
                {"title": "Количество комнат", "value": "2"},
                {"title": "Общая площадь", "value": "54 м²"},
                {"title": "Жилая площадь", "value": "30 м²"},
                {"title": "Площадь кухни", "value": "12 м²"},
                {"title": "Этаж", "value": "5 из 9"},
                {"title": "Этажей в доме", "value": "9"},
                {"title": "Тип дома", "value": "Кирпичный"},
                {"title": "Ремонт", "value": "Евроремонт"},
                {"title": "Балкон/Лоджия", "value": "Балкон"},
                {"title": "Санузел", "value": "Раздельный"},
                {"title": "Год постройки", "value": "1989"},
            ],
            "geo": item1["geo"],
            "seller": {"sellerName": "Иван", "sellerType": "private", "rating": 4.8,
                       "reviewsCount": 12, "isVerified": True},
            "counters": {"views": 1520, "contacts": 34, "favorites": 12},
            "images": [{"640x480": {"url": "https://90.img.avito.st/640x480/111.jpg"}}],
            "time": 1696200000,
            "refreshTime": 1696500000,
        }
    }
}
detail_html = (
    "<!DOCTYPE html><html><head><title>" + item1["title"] + " — Авито</title></head><body>"
    '<script type="mime/invalid" data-mfe-state="true">'
    + html.escape(json.dumps(detail, ensure_ascii=False), quote=True)
    + "</script></body></html>"
)
(OUT / "item_mfe.html").write_text(detail_html, encoding="utf-8")

# --- карточка в легаси-формате window.__initialData__ (URI-encoded) ---
legacy = {"@avito/bx-single-page": detail["loaderData"]["data"]}
legacy_html = (
    "<!DOCTYPE html><html><body><script>window.__initialData__ = \""
    + urllib.parse.quote(json.dumps(legacy, ensure_ascii=False), safe="")
    + "\";</script></body></html>"
)
(OUT / "item_initialdata.html").write_text(legacy_html, encoding="utf-8")

# --- страница выдачи с пустым каталогом ---
empty = {"loaderData": {"data": {"catalog": {"items": [], "count": 0}}}}
empty_html = (
    "<!DOCTYPE html><html><body>"
    '<script type="mime/invalid" data-mfe-state="true">'
    + html.escape(json.dumps(empty, ensure_ascii=False), quote=True)
    + "</script></body></html>"
)
(OUT / "search_empty.html").write_text(empty_html, encoding="utf-8")

# --- страница блокировки ---
(OUT / "blocked.html").write_text(
    '<!DOCTYPE html><html><head><title>Доступ ограничен: проблема с IP</title></head>'
    '<body><div class="firewall-container">Проблема с IP</div></body></html>',
    encoding="utf-8",
)

print("fixtures written to", OUT)
