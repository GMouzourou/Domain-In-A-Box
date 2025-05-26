# Domain-In-A-Box

## 1. Introduction

**Domain-In-A-Box** is a containerized solution that consolidates essential network services into a single, easy-to-deploy package. It provides an **Active Directory Domain Controller**, a **DNS server**, and a **DHCPv4 server with Dynamic DNS (DDNS)**. This makes it ideal for simplifying network management in home networks, lab environments, small to medium enterprises, and edge deployments.

### Key Features

- **Active Directory Domain Controller:**  
  Leverages Samba to offer full AD DS functionality, including centralized authentication, group policy management, and user/machine account administration.
  
- **DNS Server:**  
  Uses Bind9 to provide reliable and integrated DNS services that work seamlessly with the domain controller.
  
- **DHCPv4 Server with DDNS:**  
  Employs Kea DHCP with dynamic DNS updates, ensuring clients receive IP addresses from a specified pool while automatically updating DNS records.
  
- **Containerized Deployment:**  
  Available as a Docker image on Docker Hub, Domain-In-A-Box can be deployed using Docker CLI, Docker Compose, or orchestrated in a Kubernetes environment.
  
- **Static IP Assignment:**  
  Designed to run on a static IP within the same network as its clients, ensuring stable and predictable network configuration for directory services.
  
- **Persistent Storage:**  
  With proper volume mounts (or persistent volumes in Kubernetes), your configuration and critical data persist across container restarts and updates.

### Use Cases

- **Home Networks:**  
  Perfect for tech-savvy households seeking a centralized directory, name resolution, and IP management solution.
  
- **Lab Environments:**  
  Ideal for testing Active Directory configurations, networking practices, or learning container orchestration.
  
- **Small to Medium Enterprises:**  
  A low-overhead solution that reduces hardware requirements while delivering robust network services.
  
- **Edge Deployments:**  
  Suitable for remote sites or branch offices where a single, self-contained service bundle can simplify network management.

### Overview

Traditionally, managing a domain controller alongside DNS and DHCP requires multiple servers and complex configurations. **Domain-In-A-Box** streamlines this by packaging everything into one Docker container—reducing complexity, saving resources, and facilitating rapid deployments.

This guide is organized into four parts:
1. **Introduction:** Provides a high-level overview of the project and its features.
2. **Kubernetes:** Shows how to deploy Domain-In-A-Box in a Kubernetes cluster with persistent volumes and static IP configuration using Multus.
3. **Docker Compose:** Details deploying the container with Docker Compose, including volume bind mounts and network settings.
4. **Docker CLI:** Walks through running Domain-In-A-Box using raw Docker commands.

Each section includes guidance on configuration options that are most likely to need customization, ensuring you can tailor the deployment to your specific environment.

Below is the second part of your README.md guide—detailing how to deploy Domain-In-A-Box in a Kubernetes environment. This section explains the required prerequisites, the purpose of each Kubernetes resource, and step-by-step instructions to get started.

## 2. Kubernetes

This section describes how to deploy Domain-In-A-Box on Kubernetes. In a production-like environment, you'll leverage persistent volumes for storing configuration and data, and use Multus (or another CNI plugin) to assign a static IP address that matches your physical network.

### Prerequisites

- **Kubernetes Cluster:**  
  Ensure you have access to a Kubernetes cluster. The guide assumes you are familiar with basic cluster operations (using `kubectl`).

- **Multus CNI Plugin (or Equivalent):**  
  For static IP assignment on the same network as your clients, Multus is used to attach an additional macvlan interface to your pod. You must have a [NetworkAttachmentDefinition](https://docs.openshift.com/container-platform/4.6/networking/multiple_networks/about-multus.html) in your cluster configured for this purpose.

- **Storage Class:**  
  Your cluster should have a StorageClass (or default dynamic provisioning setup) in place to support persistent volumes, which will be used to store critical data and configuration files.

### Overview of the Kubernetes Resources

1. **NetworkAttachmentDefinition:**  
   This custom resource (CRD) is used by Multus to instruct the pod to obtain an additional interface with a static IP. Our sample configuration creates a macvlan interface that brings your pod into the same physical network as your clients.  
   - **Key elements:**  
     - `master`: Defines the host interface (e.g., `eth0`) that will be used.  
     - `ipam`: Configured in static mode with the assigned IP, subnet mask, and gateway.

2. **Headless Service:**  
   The headless Service provides stable DNS names for the StatefulSet. Because it does not allocate a cluster IP, it ensures that clients can resolve the pod’s hostname directly.

3. **StatefulSet:**  
   A StatefulSet is used because Domain-In-A-Box is a stateful application. It ensures:
   - **Unique, Stable Identity:** The pod keeps a consistent hostname and DNS entry.
   - **Persistent Storage:** Volume claim templates are defined for each of the persistent directories (for BIND, Kea, Samba, and logs).
   - **Static IP Assignment:** The pod annotations request the static IP via Multus by referencing the NetworkAttachmentDefinition.

### Step-by-Step Instructions

1. **Create the NetworkAttachmentDefinition:**

   First, apply the NetworkAttachmentDefinition YAML to your cluster. This resource is essential for the pod to be assigned a static IP on your physical network. (Customize the `master` interface and address details as needed.)

   ```yaml
   apiVersion: k8s.cni.cncf.io/v1
   kind: NetworkAttachmentDefinition
   metadata:
     name: macvlan-network
   spec:
     config: |-
       {
         "cniVersion": "0.3.1",
         "name": "macvlan-network",
         "type": "macvlan",
         "mode": "bridge",
         "master": "eth0",
         "ipam": {
           "type": "static",
           "addresses": [
             {
               "address": "192.168.1.1/24",
               "gateway": "192.168.1.254"
             }
           ]
         }
       }
   ```

   Apply this file using:

   ```bash
   kubectl apply -f <your_networkattachmentdefinition_file.yaml>
   ```

2. **Deploy the Headless Service and StatefulSet:**

   The provided YAML defines a headless Service and a StatefulSet. The Service (named `domain-controller-svc`) uses annotations to reference the `macvlan-network` and assign your static IP. The StatefulSet deploys a single replica of Domain-In-A-Box with persistent volumes via PVC templates.

   To deploy Domain-In-A-Box, run:

   ```bash
   kubectl apply -f <your_kubernetes_file.yaml>
   ```

   **What to Customize:**  
   - **Environment Variables:** Adjust the `REALM`, `DOMAIN`, `DOMAIN_PASSWORD`, and other environment variables to suit your production settings.  
   - **Ports:** Update the container ports if your application uses a different one.  
   - **Persistent Storage:** The PVC templates request 1Gi of storage by default—change the `storage` value if needed.

3. **Verifying the Deployment:**

   - **Check StatefulSet Status:**  
     Run `kubectl get statefulset` to ensure the pod is created and running.
   - **Inspect the Pod’s Network Interface:**  
     Use `kubectl describe pod <pod-name>` to confirm that the pod’s annotations request and receive the expected static IP.  
   - **Check Persistent Volumes:**  
     Use `kubectl get pvc` to see that the volume claims are bound.

### Additional Considerations

- **Security Context:**  
  The container is run with privileged access as required by Domain-In-A-Box. Evaluate if this setting fits your security policies and adjust accordingly.

- **Networking Plugins:**  
  If your Kubernetes environment does not support Multus, you may need to explore alternative networking setups (for example, using hostNetwork mode) to achieve static IP assignment.

- **Monitoring and Logging:**  
  Ensure you have mechanisms to monitor the StatefulSet and its persistent storage, as the domain controller is critical to your network's operation.

Below is the third part of your README.md guide, which explains how to deploy Domain-In-A-Box using Docker Compose. This section highlights the prerequisites, key configuration options, and step-by-step instructions to help users get started.

## 3. Docker Compose

Deploying Domain-In-A-Box using Docker Compose is a quick and efficient way to get your domain controller, DNS server, and DHCPv4/DDNS server running on a single host. The provided `docker-compose.yml` file is pre-configured with persistent volumes, static IP assignment via a macvlan network, as well as the necessary environment variables. Below are the details to help you customize and deploy the solution.

### Prerequisites

- **Docker Engine & Docker Compose:**  
  Ensure you have Docker and Docker Compose installed on your host.

- **Network Interface:**  
  Your host should have a physical network interface (e.g., `eth0`) that supports macvlan networking. Modify the `parent` property in the network configuration if your interface is named differently.

### Customization Options

The `docker-compose.yml` file includes several settings that you might want to adjust:

- **Static IP:**  
  Under the `services.domain-controller.networks.domain_net.ipv4_address` section, the static IP is set to `192.168.1.1`.  
  *Change this value if your network plan requires a different IP for the domain controller.*

- **Environment Variables:**  
  - **REALM:** Specify your Active Directory Kerberos realm (e.g., `"HOME.ARPA"`).
  - **DOMAIN:** Define your short domain name (e.g., `"HOME"`).
  - **DOMAIN_PASSWORD:** Set the initial domain administrator password.
  - **HOSTNAME:** Designate the hostname for your domain controller (e.g., `"domain-server"`).
  - **DHCP_POOL:** Specify the range of IP addresses to be leased (e.g., `"192.168.1.100-192.168.1.200"`).
  - **DNS_FORWARDERS:** List upstream DNS servers (separated by semicolons).

- **Persistent Volumes:**  
  The following named volumes are used to persist configuration and data:
  - `bind-config` → `/etc/bind`
  - `bind-data` → `/var/bind`
  - `kea-config` → `/etc/kea`
  - `kea-data` → `/var/lib/kea`
  - `samba-config` → `/etc/samba`
  - `samba-data` → `/var/lib/samba`
  - `log-data` → `/var/log`  
  *You can change these to host bind mounts if you prefer direct access to the underlying directories.*

- **Network Settings:**  
  In the `networks.domain_net` section:
  - **parent:** The host interface for macvlan (default is `eth0`).
  - **subnet & gateway:** These are set according to your local network. Adjust the `subnet` (e.g., `"192.168.1.0/24"`) and `gateway` (e.g., `"192.168.1.254"`) as needed.

### Step-by-Step Deployment

1. **Prepare the Environment:**
   - Clone or copy the repository containing the `docker-compose.yml` file.
   - Open the file and review the sections outlined above. Adjust any values (IP addresses, environment variables, volume mounts, etc.) to match your environment.

2. **Start the Container:**
   - Run the following command to launch Domain-In-A-Box in detached mode:
     ```bash
     docker-compose up -d
     ```

3. **Monitor the Deployment:**
   - Check the status of your container with:
     ```bash
     docker-compose ps
     ```
   - View real-time logs to ensure services are starting properly:
     ```bash
     docker-compose logs -f
     ```

4. **Managing the Deployment:**
   - To stop the services:
     ```bash
     docker-compose down
     ```
   - To restart:
     ```bash
     docker-compose restart
     ```

### Example `docker-compose.yml` (Excerpt)

```yaml
services:
  domain-controller:
    image: gmouzourou/domain-in-a-box:latest
    container_name: domain-in-a-box
    privileged: true
    networks:
      domain_net:
        ipv4_address: 192.168.1.1  # Change if needed to match your network plan
    environment:
      REALM: "HOME.ARPA"         # Update to match your AD realm
      DOMAIN: "HOME"             # Update with your short domain name
      DOMAIN_PASSWORD: "P@ssw0rd"  # Set your domain administrator password
      HOSTNAME: "domain-server"   # Set the hostname for your domain controller
      DHCP_POOL: "192.168.1.100-192.168.1.200"
      DNS_FORWARDERS: "1.1.1.1; 8.8.8.8;"
    volumes:
      - bind-config:/etc/bind
      - bind-data:/var/bind
      - kea-config:/etc/kea
      - kea-data:/var/lib/kea
      - samba-config:/etc/samba
      - samba-data:/var/lib/samba
      - log-data:/var/log

networks:
  domain_net:
    driver: macvlan
    driver_opts:
      parent: eth0  # Replace with your actual network interface
    ipam:
      config:
        - subnet: "192.168.1.0/24"  # Adjust to fit your local network
          gateway: "192.168.1.254"    # Change this if your gateway is different

volumes:
  bind-config:
  bind-data:
  kea-config:
  kea-data:
  samba-config:
  samba-data:
  log-data:
```
Below is the fourth section of your README.md guide, which explains how to deploy Domain-In-A-Box using the Docker CLI. This section covers the necessary prerequisites, useful commands, and options for those who prefer a straightforward, command-based deployment.

## 4. Docker CLI

Deploying Domain-In-A-Box directly via the Docker CLI is ideal for quick testing or lightweight deployments where additional orchestration features (like those provided by Compose or Kubernetes) are not required. This section outlines how to set up the necessary network, run the container with the required options, and manage the deployment.

### Prerequisites

- **Docker Engine:**  
  Ensure you have Docker installed and running on your host.

- **Sufficient Privileges:**  
  Running the container in privileged mode requires root access or appropriate permissions.

- **Macvlan Network Support:**  
  Your Docker host must support macvlan networking to assign a static IP address on the physical network. Create a custom network if it doesn't already exist.

### Step-by-Step Instructions

#### 1. Create the Docker Macvlan Network

Before running the container, create a Docker network that uses the macvlan driver. This network will allow you to assign a static IP address to the container.

```bash
docker network create -d macvlan \
  --subnet=192.168.1.0/24 \
  --gateway=192.168.1.254 \
  -o parent=eth0 \
  domain_net
```

*Adjust the subnet, gateway, and `parent` interface (e.g., `eth0`) to fit your local network configuration.*

#### 2. Run the Domain-In-A-Box Container

Use the following command to run the container with the necessary settings:

```bash
docker run -d \
  --name domain-in-a-box \
  --privileged \
  --network domain_net \
  --ip 192.168.1.1 \
  -e REALM="HOME.ARPA" \
  -e DOMAIN="HOME" \
  -e DOMAIN_PASSWORD="P@ssw0rd" \
  -e HOSTNAME="domain-server" \
  -e DHCP_POOL="192.168.1.100-192.168.1.200" \
  -e DNS_FORWARDERS="1.1.1.1; 8.8.8.8;" \
  -v bind-config:/etc/bind \
  -v bind-data:/var/bind \
  -v kea-config:/etc/kea \
  -v kea-data:/var/lib/kea \
  -v samba-config:/etc/samba \
  -v samba-data:/var/lib/samba \
  -v log-data:/var/log \
  gmouzourou/domain-in-a-box:latest
```

**Explanation of key options:**

- **`-d`**: Runs the container in detached mode.
- **`--name domain-in-a-box`**: Assigns a custom name to the container.
- **`--privileged`**: Grants the container elevated privileges for necessary low-level operations.
- **`--network domain_net` & `--ip 192.168.1.1`**: Connects the container to the custom macvlan network and assigns it a static IP address.
- **Environment Variables (`-e`)**: Passes configuration values such as the Kerberos realm, domain details, DHCP pool, and DNS forwarders.
- **Volume Mounts (`-v`)**: Ensures persistent data storage by mounting named volumes to directories inside the container.

#### 3. Verify and Manage the Deployment

- **Check Container Status:**

  ```bash
  docker ps
  ```

- **Review Container Logs:**

  ```bash
  docker logs domain-in-a-box
  ```

- **Access the Container for Debugging (if needed):**

  ```bash
  docker exec -it domain-in-a-box /bin/sh
  ```

- **Stop and Remove the Container:**

  To stop and remove the container, run:

  ```bash
  docker stop domain-in-a-box && docker rm domain-in-a-box
  ```
  
- **Remove the Created Network (Optional):**

  If you no longer need the macvlan network:

  ```bash
  docker network rm domain_net
  ```

### Additional Considerations

- **Persistent Data:**  
  If you prefer to use host directories (bind mounts) instead of Docker-managed named volumes, replace the volume options with absolute paths (e.g., `/path/to/host/bind-config:/etc/bind`).

- **Security Implications:**  
  Running the container in privileged mode and assigning a static IP on your host network may introduce security risks. Ensure that your host is secure and that only trusted users have access.

- **Environment-Specific Adjustments:**  
  Customize environment variables and network settings to match your production environment. These provide flexibility to adjust for different network topologies or security requirements.
