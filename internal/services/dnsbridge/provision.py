import json
import sys

import ldb
from samba.auth import system_session
from samba.ndr import ndr_unpack
from samba.dcerpc import security
from samba.param import LoadParm
from samba.samdb import SamDB
from samba.sd_utils import SDUtils


def provision(request):
    cfg = request["config"]
    lp = LoadParm()
    lp.load("/etc/samba/smb.conf")
    if lp.get("realm").upper() != cfg["realm"]:
        raise ValueError("bridge realm does not match this domain controller")
    samdb = SamDB(url=lp.private_path("sam.ldb"), session_info=system_session(), lp=lp)
    sdutils = SDUtils(samdb)
    tag = f'Domain-In-A-Box ExternalDNS {cfg["ownerID"]} zone {cfg["zone"]}'
    account_filter = f'(sAMAccountName={cfg["username"]})'
    committed = False
    samdb.transaction_start()
    try:
        zones = []
        contexts = samdb.search(base="", scope=ldb.SCOPE_BASE, attrs=["namingContexts"])[0]["namingContexts"]
        for context in contexts:
            context = context.decode("utf-8")
            if context.lower().startswith(("dc=domaindnszones,", "dc=forestdnszones,")):
                zones.extend(samdb.search(
                    base=f"CN=MicrosoftDNS,{context}", scope=ldb.SCOPE_ONELEVEL,
                    expression="(objectClass=dnsZone)", attrs=["name"]))
        managed = [zone for zone in zones if zone["name"][0].decode("utf-8").lower() == cfg["zone"]]
        if len(managed) != 1:
            raise ValueError("managed DNS zone must already exist exactly once")

        accounts = samdb.search(expression=account_filter, attrs=["description"])
        if not accounts:
            samdb.newuser(cfg["username"], request["password"], description=tag, useusernameascn=True)
        elif len(accounts) != 1 or [value.decode("utf-8") for value in accounts[0].get("description", [])] != [tag]:
            raise ValueError("refusing to adopt an existing account with different ownership metadata")
        account = samdb.search(
            expression=account_filter,
            attrs=["objectSid", "primaryGroupID", "memberOf", "userAccountControl"])[0]
        flags = int(account["userAccountControl"][0])
        if int(account["primaryGroupID"][0]) != 513 or account.get("memberOf") or not flags & 512 or flags & (2 | 256 | 4096 | 8192):
            raise ValueError("bridge identity must be an enabled, dedicated ordinary Domain Users account")
        sid = str(ndr_unpack(security.dom_sid, bytes(account["objectSid"][0])))
        readonly = f"(D;;SDCCDCWPWDWO;;;{sid})"
        protected = set(request["protected"])
        guarded = 0
        for zone in zones:
            if zone.dn != managed[0].dn:
                sdutils.dacl_prepend_aces(zone.dn, f"(D;;GA;;;{sid})")
                for node in samdb.search(base=zone.dn, scope=ldb.SCOPE_SUBTREE, expression="(objectClass=dnsNode)", attrs=["name"]):
                    sdutils.dacl_prepend_aces(node.dn, f"(D;;GA;;;{sid})")
                continue
            sdutils.dacl_prepend_aces(zone.dn, f"(D;;SDDCWPWDWO;;;{sid})")
            sdutils.dacl_append_aces(zone.dn, f"(A;;CC;;;{sid})")
            nodes = samdb.search(
                base=zone.dn, scope=ldb.SCOPE_SUBTREE,
                expression="(objectClass=dnsNode)", attrs=["name"])
            for node in nodes:
                label = node["name"][0].decode("utf-8").lower()
                name = cfg["zone"] if label == "@" else f'{label}.{cfg["zone"]}'
                descriptor = sdutils.read_sd_on_dn(node.dn)
                if str(descriptor.owner_sid) != sid or name in protected:
                    sdutils.dacl_prepend_aces(node.dn, readonly)
                    guarded += 1
        samdb.transaction_commit()
        committed = True
        return {"account": cfg["username"], "zone": cfg["zone"], "guardedNodes": guarded, "zonesReviewed": len(zones)}
    finally:
        if not committed:
            samdb.transaction_cancel()


if __name__ == "__main__":
    print(json.dumps(provision(json.load(sys.stdin))))
