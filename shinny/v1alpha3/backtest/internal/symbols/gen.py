"""把 tqsdk-python 的 expired_quotes.json.lzma 转为 Go 可直接读取的 gzip JSON。

源文件为旧式 .lzma（LZMA_Alone）且含 NaN（非法 JSON），Go 标准库无法直接读取；
本脚本一次性转换：去掉值为 NaN 的字段，其余字段与键名保持不变。

用法（在本目录）：python3 gen.py /path/to/tqsdk/expired_quotes.json.lzma
"""
import gzip
import hashlib
import json
import lzma
import math
import sys

src = sys.argv[1]
raw = open(src, "rb").read()
quotes = json.loads(lzma.decompress(raw))
clean = {sym: {k: v for k, v in q.items() if not (isinstance(v, float) and math.isnan(v))}
         for sym, q in sorted(quotes.items())}
data = json.dumps(clean, ensure_ascii=False, separators=(",", ":"), sort_keys=True).encode()
with gzip.GzipFile("expired_quotes.json.gz", "wb", mtime=0) as f:
    f.write(data)
with open("SOURCE", "w") as f:
    f.write("tqsdk-python 3.10.4 tqsdk/expired_quotes.json.lzma\n")
    f.write("sha256 %s\n" % hashlib.sha256(raw).hexdigest())
    f.write("contracts %d\n" % len(clean))
