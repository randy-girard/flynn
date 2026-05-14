# -*- mode: ruby -*-
# vi: set ft=ruby :

# Fail if Vagrant version is too old
begin
  Vagrant.require_version ">= 1.9.0"
rescue NoMethodError
  $stderr.puts "This Vagrantfile requires Vagrant version >= 1.9.0"
  exit 1
end

# Vagrantfile API/syntax version. Don't touch unless you know what you're doing!
VAGRANTFILE_API_VERSION = "2"

Vagrant.configure(VAGRANTFILE_API_VERSION) do |config|
  #config.vm.box = "ubuntu/xenial64"
  config.vm.box = "bento/ubuntu-24.04"

  config.ssh.forward_agent = true

  # Sync all project directories to the VM (owned by root)
  config.vm.synced_folder ".", "/root/go/src/github.com/flynn/flynn", create: true, group: "root", owner: "root"

  if Vagrant.has_plugin?("vagrant-vbguest")
    # vagrant-vbguest can cause the VM to not start: https://github.com/flynn/flynn/issues/2874
    config.vbguest.auto_update = false
  end

  # Override locale settings. Avoids host locale settings being sent via SSH
  ENV['LC_ALL']="en_US.UTF-8"
  ENV['LANG']="en_US.UTF-8"
  ENV['LANGUAGE']="en_US.UTF-8"

  # HTTP/HTTPS: bind on all host interfaces so another machine (e.g. a Caddy
  # reverse proxy) on the LAN can reach the VM. If you only browse from the host,
  # set host_ip back to "127.0.0.1" for stricter binding.
  #
  # Flynn router HTTP is guest:80 → host:8080 (not host:80). Point your proxy at
  # http://<flynn-host-lan-ip>:8080 for plain HTTP to the router.
  config.vm.define "builder" do |builder|
    builder.vm.hostname = "builder"

    builder.disksize.size = "80GB"  
    builder.vm.provider "virtualbox" do |v, override|
      v.memory = ENV["VAGRANT_MEMORY"] || 24000  # Increased for running multiple services
      v.cpus = ENV["VAGRANT_CPUS"] || 8

      # Enable nested virtualization if needed for containers
      v.customize ["modifyvm", :id, "--nested-hw-virt", "on"]
    end

    # Display helpful information after provisioning
    builder.vm.provision "shell", privileged: true, inline: <<-SHELL
        sudo su -l

        cd /root/go/src/github.com/flynn/flynn
        ./setup.sh
    SHELL

    builder.vm.network "private_network", ip: "192.168.56.10"
  end

  config.vm.define "runner" do |runner|
    runner.vm.hostname = "runner"

    runner.disksize.size = "80GB"  
    runner.vm.provider "virtualbox" do |v, override|
      v.memory = ENV["VAGRANT_MEMORY"] || 24000  # Increased for running multiple services
      v.cpus = ENV["VAGRANT_CPUS"] || 8

      # Enable nested virtualization if needed for containers
      v.customize ["modifyvm", :id, "--nested-hw-virt", "on"]
    end

    runner.vm.network "private_network", ip: "192.168.56.11"
    runner.vm.network "forwarded_port", guest: 80, host: 8080
    runner.vm.network "forwarded_port", guest: 443, host: 8443

    runner.vm.provision "shell", privileged: true, inline: <<-SHELL
      sudo su -l
      apt-get update
      #apt-get install -y lvm2

      growpart /dev/sda 3
      pvresize /dev/sda3
      lvextend -l +100%FREE -r /dev/ubuntu-vg/ubuntu-lv
    SHELL
  end

  # VAGRANT_MEMORY          - instance memory, in MB
  # VAGRANT_CPUS            - instance virtual CPUs

end
