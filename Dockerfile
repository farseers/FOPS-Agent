FROM steden88/cicd:3.0
WORKDIR /app
# 复制配置（没有配置需要注释掉）
COPY /fops/fops-agent .

#设置时区
RUN ln -sf /usr/share/zoneinfo/Asia/Shanghai    /etc/localtime

WORKDIR /app
ENTRYPOINT ["./fops-agent"]