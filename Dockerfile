FROM alpine:3.16.9

RUN apk add --no-cache bind kea-dhcp4 kea-dhcp-ddns samba-dc supervisor ed

ADD supervisord.conf /etc/supervisord.conf
ADD entrypoint.sh /entrypoint.sh

RUN chmod 755 /entrypoint.sh && \
    mkdir -p /run/kea && \
    chmod 777 /run/kea && \
    rm /etc/samba/smb.conf

EXPOSE 53 53/udp 67/udp 68/udp 88 135 138 139 389 445 464 636 3268 3269

ENTRYPOINT ["/entrypoint.sh"]