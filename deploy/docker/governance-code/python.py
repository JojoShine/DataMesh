import sys, json, traceback, types, decimal
from decimal import Decimal

decimal.getcontext().prec = 76
decimal.getcontext().rounding = decimal.ROUND_HALF_EVEN
protocol = sys.stdout
sys.stdout = sys.stderr

def encode(value):
    if isinstance(value, Decimal):
        return str(value)
    raise TypeError("Unsupported output type")

def send(value):
    protocol.write(json.dumps(value, default=encode, ensure_ascii=False, allow_nan=False, separators=(",", ":")) + "\n")
    protocol.flush()

def failure(error):
    line, column = 0, 0
    if isinstance(error, SyntaxError):
        line, column = error.lineno or 0, error.offset or 0
    else:
        for frame in traceback.extract_tb(error.__traceback__):
            if frame.filename == "<user_code>":
                line = frame.lineno
    code = "SCRIPT_ERROR"
    for kind in (SyntaxError, TypeError, ValueError, NameError, ZeroDivisionError, OverflowError, IndexError, KeyError, AttributeError, MemoryError, RecursionError, ImportError):
        if isinstance(error, kind):
            code = "SCRIPT_" + kind.__name__.upper()
            break
    return {"code": code, "line": line, "column": column}

try:
    config = json.loads(sys.stdin.readline())
    scope = {"Decimal": Decimal, "__name__": "__user_code__"}
    exec(compile(config["source"], "<user_code>", "exec"), scope)
    transform = scope.get("transform")
    if not callable(transform):
        raise TypeError("Missing transform")
    context = types.MappingProxyType(config.get("context") or {})
    send({"type": "ready", "sequence": 0})
except BaseException as error:
    send({"type": "ready", "sequence": 0, "error": failure(error)})
    sys.exit(1)

for line in sys.stdin:
    try:
        batch = json.loads(line)
        result = []
        for row in batch["rows"]:
            values = transform(dict(row["values"]), context)
            if not isinstance(values, dict) or not all(isinstance(key, str) for key in values):
                raise TypeError("Expected one object per input")
            result.append({"ordinal": row["ordinal"], "values": values})
        send({"type": "batch", "sequence": batch["sequence"], "rows": result})
    except BaseException as error:
        send({"type": "batch", "sequence": batch.get("sequence", 0), "error": failure(error)})
        sys.exit(1)
