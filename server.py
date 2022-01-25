from flask import Flask
app = Flask(__name__)
import sys

server_name = sys.argv[1]

@app.route('/')
def main():
    return server_name

if __name__ == '__main__':
   app.run(port=sys.argv[2])
