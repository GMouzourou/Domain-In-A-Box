import importlib.util
import logging
import shutil
import tempfile

import base64
import ldb
from samba.auth import system_session, user_session
from samba.provision import provision
from samba.samdb import SamDB
from samba.sd_utils import SDUtils


def main():
    with tempfile.TemporaryDirectory(prefix="dib-native-test-") as directory:
        result = provision(
            logging.getLogger("native-test"), system_session(), targetdir=directory,
            realm="BRIDGE.TEST.ARPA", domain="BRIDGE", hostname="dc1", hostip="198.18.80.8",
            serverrole="active directory domain controller", dns_backend="SAMBA_INTERNAL",
            adminpass="Disposable-Native123!", useeadb=True, skip_sysvolacl=True)
        shutil.copyfile(f"{directory}/etc/smb.conf", "/etc/samba/smb.conf")
        samdb = SamDB(url=result.lp.private_path("sam.ldb"), session_info=system_session(), lp=result.lp)
        sdutils = SDUtils(samdb)
        base = "CN=MicrosoftDNS,DC=DomainDnsZones," + str(samdb.domain_dn())
        zone = samdb.search(base=base, expression="(&(objectClass=dnsZone)(name=bridge.test.arpa))")[0].dn
        marker = "S-1-5-21-42-43-44-999"
        sdutils.dacl_prepend_aces(zone, f"(A;;RP;;;{marker})")
        spec = importlib.util.spec_from_file_location("bridge_provision", "/native/provision.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        request = {
            "config": {"realm": "BRIDGE.TEST.ARPA", "zone": "bridge.test.arpa",
                       "username": "dib-edns", "ownerID": "native-test"},
            "password": "Disposable-Bridge123!",
            "protected": ["bridge.test.arpa", "dc1.bridge.test.arpa", "wpad.bridge.test.arpa"],
        }
        first = module.provision(request)
        before = sdutils.get_sd_as_sddl(zone)
        second = module.provision(request)
        after = sdutils.get_sd_as_sddl(zone)
        if first != second or before != after or marker not in after:
            raise AssertionError("provisioning was not idempotent or changed unrelated explicit ACEs")
        conflicting = {**request, "config": {**request["config"], "ownerID": "different-owner"}}
        try:
            module.provision(conflicting)
        except ValueError as error:
            if "refusing to adopt" not in str(error):
                raise
        else:
            raise AssertionError("existing identity was silently adopted")
        if sdutils.get_sd_as_sddl(zone) != after:
            raise AssertionError("failed provisioning changed the zone ACL")
        account = samdb.search(
            expression="(sAMAccountName=dib-edns)", attrs=["primaryGroupID"])[0]
        if int(account["primaryGroupID"][0]) != 513:
            raise AssertionError("provisioning did not create an ordinary Domain Users identity")
        identity = user_session(samdb, result.lp, "dib-edns")
        userdb = SamDB(url=result.lp.private_path("sam.ldb"), session_info=identity, lp=result.lp)
        record = base64.b64decode("BAABAAXwAAAAAAAAAAADhAAAAAAAAAAAxhIAMg==")
        userdb.add({"dn": f"DC=native-created,{zone}", "objectClass": "dnsNode", "dnsRecord": record})
        module.provision(request)
        message = ldb.Message()
        message.dn = ldb.Dn(userdb, f"DC=native-created,{zone}")
        message["dnsRecord"] = ldb.MessageElement(record, ldb.FLAG_MOD_REPLACE, "dnsRecord")
        userdb.modify(message)
        outside = f"DC=_msdcs.bridge.test.arpa,CN=MicrosoftDNS,DC=ForestDnsZones,{samdb.domain_dn()}"
        for dn in (f"DC=forbidden,{outside}", f"DC=dc1,{zone}"):
            try:
                if dn.startswith("DC=forbidden,"):
                    userdb.add({"dn": dn, "objectClass": "dnsNode", "dnsRecord": record})
                else:
                    message.dn = ldb.Dn(userdb, dn)
                    userdb.modify(message)
            except ldb.LdbError as error:
                if error.args[0] not in (ldb.ERR_INSUFFICIENT_ACCESS_RIGHTS, ldb.ERR_NO_SUCH_OBJECT):
                    raise
            else:
                raise AssertionError(f"identity escaped its direct DNS authority boundary: {dn}")
        print("PASS: actual ordinary-identity directory create/update and outside-zone/manual-node denial")
        print("PASS: native provisioning, ownership refusal, transaction rollback, idempotence and ACE preservation")


if __name__ == "__main__":
    main()
