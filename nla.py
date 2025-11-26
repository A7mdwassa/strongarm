import asyncio
import struct
from tqdm.asyncio import tqdm_asyncio


# ---------------------------
# RDP NEGOTIATION CONSTANTS
# ---------------------------
TYPE_RDP_NEG_REQ = 1
PROTOCOL_RDP = 0
PROTOCOL_SSL = 1
PROTOCOL_HYBRID = 2

TDPU_CONNECTION_REQUEST = 0xE0


# --------------------------------------------------------
# Build RDP Negotiation Request (TPKT + TPDU + RDP_NEG_REQ)
# --------------------------------------------------------
def build_rdp_neg_request():
    # RDP_NEG_REQ structure
    rdp = struct.pack("<BBHL",
                      TYPE_RDP_NEG_REQ,   # Type
                      0,                  # Flags
                      8,                  # Length
                      PROTOCOL_SSL | PROTOCOL_HYBRID)

    # TPDU Connection Request (0xE0)
    tpdu = struct.pack("!BBH", 0x00, TDPU_CONNECTION_REQUEST, len(rdp)) + rdp

    # TPKT Header
    tpkt = struct.pack("!BBH", 3, 0, len(tpdu) + 4) + tpdu

    return tpkt


# ---------------------------
# Parse RDP Response
# ---------------------------
def parse_rdp_response(data: bytes):
    if len(data) < 11:
        return None

    # Skip TPKT (4 bytes), TPDU header (3 bytes)
    rdp_type = data[7]

    # Type = 2 → NLA required
    if rdp_type == 2:
        return "NLA"

    # Type = 1 → SSL only
    if rdp_type == 1:
        return "SSL"

    # If none → standard RDP
    return "RDP"


# ---------------------------
# Async RDP Check
# ---------------------------
async def check_rdp(host: str, port: int, timeout=5):
    pkt = build_rdp_neg_request()

    try:
        reader, writer = await asyncio.wait_for(
            asyncio.open_connection(host, port),
            timeout=timeout
        )

        writer.write(pkt)
        await writer.drain()

        data = await asyncio.wait_for(reader.read(1024), timeout=timeout)
        writer.close()
        await writer.wait_closed()

        return parse_rdp_response(data)

    except asyncio.TimeoutError:
        return "TIMEOUT"
    except Exception:
        return "ERROR"


# ---------------------------
# Worker for each host
# ---------------------------
async def worker(line, results):
    try:
        host, port = line.split(":")
        result = await check_rdp(host, int(port))
        results[result].append(line)
    except Exception:
        results["ERROR"].append(line)


# ---------------------------
# Main Async Runner
# ---------------------------
async def main():
    print("Enter input filename with IP:PORT list:")
    fln = input("> ").strip()

    with open(fln) as f:
        targets = [x.strip() for x in f if x.strip()]

    print("Number of async workers:")
    workers = int(input("> "))

    semaphore = asyncio.Semaphore(workers)

    results = {
        "NLA": [],
        "SSL": [],
        "RDP": [],
        "TIMEOUT": [],
        "ERROR": []
    }

    async def sem_task(target):
        async with semaphore:
            await worker(target, results)

    await tqdm_asyncio.gather(*(sem_task(t) for t in targets))

    # Output results
    for name in results:
        with open(f"{name}.txt", "a") as f:
            f.write("\n".join(results[name]) + "\n")

    print("\nScan complete. Results written to:")
    print("  NLA.txt")
    print("  SSL.txt")
    print("  RDP.txt")
    print("  TIMEOUT.txt")
    print("  ERROR.txt")


# ---------------------------
# Start
# ---------------------------
if __name__ == "__main__":
    asyncio.run(main())
