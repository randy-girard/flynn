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

  # Sync all project directories to the VM (owned by root)
  config.vm.synced_folder ".", "/root/go/src/github.com/flynn/flynn", create: true, group: "root", owner: "root"
  config.vm.synced_folder "./flynn-discovery", "/root/go/src/github.com/flynn/flynn-discovery", create: true, group: "root", owner: "root"
  config.vm.synced_folder "./go-tuf", "/root/go/src/github.com/flynn/go-tuf", create: true, group: "root", owner: "root"

  if Vagrant.has_plugin?("vagrant-vbguest")
    # vagrant-vbguest can cause the VM to not start: https://github.com/flynn/flynn/issues/2874
    config.vbguest.auto_update = false
  end

  # Override locale settings. Avoids host locale settings being sent via SSH
  ENV['LC_ALL']="en_US.UTF-8"
  ENV['LANG']="en_US.UTF-8"
  ENV['LANGUAGE']="en_US.UTF-8"

  # Network configuration for services
  # Flynn Discovery
  config.vm.network "forwarded_port", guest: 1111, host: 1111, host_ip: "127.0.0.1"
  # Repository Service TUF API
  config.vm.network "forwarded_port", guest: 80, host: 8000, host_ip: "127.0.0.1"
  # Repository Service TUF Web Server
  config.vm.network "forwarded_port", guest: 8080, host: 8080, host_ip: "127.0.0.1"
  # PostgreSQL
  config.vm.network "forwarded_port", guest: 5432, host: 15432, host_ip: "127.0.0.1"
  config.vm.network "forwarded_port", guest: 5433, host: 15433, host_ip: "127.0.0.1"
  # Redis
  config.vm.network "forwarded_port", guest: 6379, host: 16379, host_ip: "127.0.0.1"
  # MongoDB
  config.vm.network "forwarded_port", guest: 27017, host: 27017, host_ip: "127.0.0.1"

  # VAGRANT_MEMORY          - instance memory, in MB
  # VAGRANT_CPUS            - instance virtual CPUs
  config.vm.provider "virtualbox" do |v, override|
    v.memory = ENV["VAGRANT_MEMORY"] || 32786  # Increased for running multiple services
    v.cpus = ENV["VAGRANT_CPUS"] || 8

    # Enable nested virtualization if needed for containers
    v.customize ["modifyvm", :id, "--nested-hw-virt", "on"]
  end

  # Provision with Ansible (builds everything and installs to /usr/local/flynn/bin)
  #config.vm.provision "ansible" do |ansible|
  #  ansible.playbook = "playbook.yml"
  #  ansible.config_file = "ansible.cfg"
  #end

  # Display helpful information after provisioning
  config.vm.provision "shell", privileged: true, inline: <<-SHELL
      #sudo su -l

      #cd /root/go/src/github.com/flynn/flynn
      #./prereq.sh
      #./setup.sh
      #./build.sh

      #}export FLYNN_HOST_CHECKSUM=d46b42b1fed7046ca78bb2eee566ba3dd20bc996f138ba438e1609f1565971e677414d5d3ad89e13cc2b1a466aaa93c0b1e77e6a39577831e8bff25ff7ff647d
      #/usr/bin/install-flynn --repo https://dl.flynn.cloud.randygirard.com --version dev


      exit
  SHELL
end
